package badger

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	badgerdb "github.com/dgraph-io/badger/v4"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/nobelk/reverb/pkg/store"
)

const tracerName = "github.com/nobelk/reverb/pkg/store/badger"

const (
	prefixHash    = "hash:"
	prefixLineage = "lineage:"
)

// Store is a BadgerDB-backed persistent store.
type Store struct {
	db *badgerdb.DB
}

// New opens (or creates) a BadgerDB database at the given path.
func New(path string) (*Store, error) {
	opts := badgerdb.DefaultOptions(path).WithLogger(nil)
	db, err := badgerdb.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("badger open: %w", err)
	}
	return &Store{db: db}, nil
}

// NewInMemory opens an in-memory BadgerDB instance (useful for tests).
func NewInMemory() (*Store, error) {
	opts := badgerdb.DefaultOptions("").WithInMemory(true).WithLogger(nil)
	db, err := badgerdb.Open(opts)
	if err != nil {
		return nil, fmt.Errorf("badger open in-memory: %w", err)
	}
	return &Store{db: db}, nil
}

func entryKey(id string) []byte {
	return []byte("entry:" + id)
}

func hashIndexKey(namespace string, hash [32]byte) []byte {
	return []byte(prefixHash + namespace + ":" + hex.EncodeToString(hash[:]))
}

func lineageKey(sourceID, entryID string) []byte {
	return []byte(prefixLineage + sourceID + ":" + entryID)
}

func lineagePrefix(sourceID string) []byte {
	return []byte(prefixLineage + sourceID + ":")
}

func (s *Store) Get(ctx context.Context, id string) (*store.CacheEntry, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.store.get")
	defer span.End()
	span.SetAttributes(attribute.String("gen_ai.system", "reverb"), attribute.String("gen_ai.cache.store.backend", "badger"), attribute.String("gen_ai.cache.entry_id", id))

	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	var entry *store.CacheEntry
	err := s.db.View(func(txn *badgerdb.Txn) error {
		item, err := txn.Get(entryKey(id))
		if err != nil {
			if errors.Is(err, badgerdb.ErrKeyNotFound) {
				return nil
			}
			return err
		}
		return item.Value(func(val []byte) error {
			var e store.CacheEntry
			if err := json.Unmarshal(val, &e); err != nil {
				return err
			}
			entry = &e
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

func (s *Store) GetByHash(ctx context.Context, namespace string, hash [32]byte) (*store.CacheEntry, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.store.get_by_hash")
	defer span.End()
	span.SetAttributes(attribute.String("gen_ai.system", "reverb"), attribute.String("gen_ai.cache.store.backend", "badger"), attribute.String("gen_ai.cache.namespace", namespace))

	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	var entry *store.CacheEntry
	err := s.db.View(func(txn *badgerdb.Txn) error {
		item, err := txn.Get(hashIndexKey(namespace, hash))
		if err != nil {
			if errors.Is(err, badgerdb.ErrKeyNotFound) {
				return nil
			}
			return err
		}
		var entryID string
		if err := item.Value(func(val []byte) error {
			entryID = string(val)
			return nil
		}); err != nil {
			return err
		}
		eItem, err := txn.Get(entryKey(entryID))
		if err != nil {
			if errors.Is(err, badgerdb.ErrKeyNotFound) {
				return nil
			}
			return err
		}
		return eItem.Value(func(val []byte) error {
			var e store.CacheEntry
			if err := json.Unmarshal(val, &e); err != nil {
				return err
			}
			entry = &e
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

func (s *Store) Put(ctx context.Context, entry *store.CacheEntry) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.store.put")
	defer span.End()
	span.SetAttributes(attribute.String("gen_ai.system", "reverb"), attribute.String("gen_ai.cache.store.backend", "badger"), attribute.String("gen_ai.cache.entry_id", entry.ID), attribute.String("gen_ai.cache.namespace", entry.Namespace))

	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal entry: %w", err)
	}

	return s.db.Update(func(txn *badgerdb.Txn) error {
		// Check for existing entry to clean up old indices
		item, err := txn.Get(entryKey(entry.ID))
		if err == nil {
			if err := item.Value(func(val []byte) error {
				var old store.CacheEntry
				if err := json.Unmarshal(val, &old); err != nil {
					return err
				}
				// Remove old hash index
				if err := txn.Delete(hashIndexKey(old.Namespace, old.PromptHash)); err != nil && !errors.Is(err, badgerdb.ErrKeyNotFound) {
					return err
				}
				// Remove old lineage indices
				for _, src := range old.SourceHashes {
					if err := txn.Delete(lineageKey(src.SourceID, old.ID)); err != nil && !errors.Is(err, badgerdb.ErrKeyNotFound) {
						return err
					}
				}
				return nil
			}); err != nil {
				return err
			}
		} else if !errors.Is(err, badgerdb.ErrKeyNotFound) {
			return err
		}

		// Write entry
		if err := txn.Set(entryKey(entry.ID), data); err != nil {
			return err
		}
		// Write hash index
		if err := txn.Set(hashIndexKey(entry.Namespace, entry.PromptHash), []byte(entry.ID)); err != nil {
			return err
		}
		// Write lineage indices
		for _, src := range entry.SourceHashes {
			if err := txn.Set(lineageKey(src.SourceID, entry.ID), []byte{}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) Delete(ctx context.Context, id string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.store.delete")
	defer span.End()
	span.SetAttributes(attribute.String("gen_ai.system", "reverb"), attribute.String("gen_ai.cache.store.backend", "badger"), attribute.String("gen_ai.cache.entry_id", id))

	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return s.db.Update(func(txn *badgerdb.Txn) error {
		item, err := txn.Get(entryKey(id))
		if err != nil {
			if errors.Is(err, badgerdb.ErrKeyNotFound) {
				return nil
			}
			return err
		}
		return item.Value(func(val []byte) error {
			var entry store.CacheEntry
			if err := json.Unmarshal(val, &entry); err != nil {
				return err
			}
			if err := txn.Delete(entryKey(id)); err != nil {
				return err
			}
			if err := txn.Delete(hashIndexKey(entry.Namespace, entry.PromptHash)); err != nil && !errors.Is(err, badgerdb.ErrKeyNotFound) {
				return err
			}
			for _, src := range entry.SourceHashes {
				if err := txn.Delete(lineageKey(src.SourceID, id)); err != nil && !errors.Is(err, badgerdb.ErrKeyNotFound) {
					return err
				}
			}
			return nil
		})
	})
}

func (s *Store) DeleteBatch(ctx context.Context, ids []string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.store.delete_batch")
	defer span.End()
	span.SetAttributes(attribute.String("gen_ai.system", "reverb"), attribute.String("gen_ai.cache.store.backend", "badger"), attribute.Int("gen_ai.cache.batch_size", len(ids)))

	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	for _, id := range ids {
		if err := s.Delete(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListBySource(ctx context.Context, sourceID string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	prefix := lineagePrefix(sourceID)
	var ids []string
	err := s.db.View(func(txn *badgerdb.Txn) error {
		opts := badgerdb.DefaultIteratorOptions
		opts.PrefetchValues = false
		opts.Prefix = prefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			key := it.Item().Key()
			// key is "lineage:{sourceID}:{entryID}"
			entryID := string(key[len(prefix):])
			ids = append(ids, entryID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}

func (s *Store) IncrementHit(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.db.Update(func(txn *badgerdb.Txn) error {
		item, err := txn.Get(entryKey(id))
		if err != nil {
			if errors.Is(err, badgerdb.ErrKeyNotFound) {
				return nil
			}
			return err
		}
		return item.Value(func(val []byte) error {
			var entry store.CacheEntry
			if err := json.Unmarshal(val, &entry); err != nil {
				return err
			}
			entry.HitCount++
			entry.LastHitAt = time.Now()
			data, err := json.Marshal(&entry)
			if err != nil {
				return err
			}
			return txn.Set(entryKey(id), data)
		})
	})
}

func (s *Store) Scan(ctx context.Context, namespace string, fn func(*store.CacheEntry) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entryPrefix := []byte("entry:")
	err := s.db.View(func(txn *badgerdb.Txn) error {
		opts := badgerdb.DefaultIteratorOptions
		opts.Prefix = entryPrefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Seek(entryPrefix); it.ValidForPrefix(entryPrefix); it.Next() {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var entry store.CacheEntry
			if err := it.Item().Value(func(val []byte) error {
				return json.Unmarshal(val, &entry)
			}); err != nil {
				return err
			}
			if entry.Namespace != namespace {
				continue
			}
			if !fn(&entry) {
				break
			}
		}
		return nil
	})
	return err
}

func (s *Store) Stats(ctx context.Context) (*store.StoreStats, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entryPrefix := []byte("entry:")
	var total int64
	nsSet := make(map[string]struct{})
	err := s.db.View(func(txn *badgerdb.Txn) error {
		opts := badgerdb.DefaultIteratorOptions
		opts.Prefix = entryPrefix
		it := txn.NewIterator(opts)
		defer it.Close()
		for it.Seek(entryPrefix); it.ValidForPrefix(entryPrefix); it.Next() {
			var entry store.CacheEntry
			if err := it.Item().Value(func(val []byte) error {
				return json.Unmarshal(val, &entry)
			}); err != nil {
				return err
			}
			total++
			nsSet[entry.Namespace] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	namespaces := make([]string, 0, len(nsSet))
	for ns := range nsSet {
		namespaces = append(namespaces, ns)
	}
	return &store.StoreStats{
		TotalEntries: total,
		Namespaces:   namespaces,
	}, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
