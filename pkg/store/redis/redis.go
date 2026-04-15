package redis

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/nobelk/reverb/pkg/store"
)

const tracerName = "github.com/nobelk/reverb/pkg/store/redis"

// incrementHitScript atomically reads the entry JSON, increments HitCount,
// updates LastHitAt, and writes it back — all inside a single Redis eval.
var incrementHitScript = goredis.NewScript(`
local data = redis.call('HGET', KEYS[1], 'data')
if data == false then
  return 0
end
local entry = cjson.decode(data)
entry['HitCount'] = (entry['HitCount'] or 0) + 1
entry['LastHitAt'] = ARGV[1]
redis.call('HSET', KEYS[1], 'data', cjson.encode(entry))
return 1
`)

// putScript atomically:
//   1. Reads old entry; if found, removes stale hash index and lineage memberships.
//   2. Writes new entry data, new hash index, and new lineage memberships.
//
// KEYS:  [1]=entryKey  [2]=newHashKey
// ARGV:  [1]=oldHashKey  [2]=entryJSON  [3]=entryID
//        [4]=removeCount  [5..4+removeCount]=lineageKeysToRemove
//        [4+removeCount+1]=addCount  [4+removeCount+2..]=lineageKeysToAdd
var putScript = goredis.NewScript(`
local entry_key    = KEYS[1]
local new_hash_key = KEYS[2]
local old_hash_key = ARGV[1]
local entry_json   = ARGV[2]
local entry_id     = ARGV[3]
local remove_count = tonumber(ARGV[4])

local old_data = redis.call('HGET', entry_key, 'data')
if old_data ~= false then
  if old_hash_key ~= new_hash_key then
    redis.call('DEL', old_hash_key)
  end
  for i = 1, remove_count do
    redis.call('SREM', ARGV[4 + i], entry_id)
  end
end

redis.call('HSET', entry_key, 'data', entry_json)
redis.call('SET', new_hash_key, entry_id)

local add_base  = 4 + remove_count + 1
local add_count = tonumber(ARGV[add_base])
for i = 1, add_count do
  redis.call('SADD', ARGV[add_base + i], entry_id)
end
return 1
`)

// Store is a Redis-backed cache store.
type Store struct {
	client *goredis.Client
	prefix string
}

// New creates a new Redis store.
func New(addr, password string, db int, prefix string) (*Store, error) {
	client := goredis.NewClient(&goredis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	if prefix == "" {
		prefix = "reverb:"
	}
	return &Store{client: client, prefix: prefix}, nil
}

func (s *Store) entryKey(id string) string {
	return s.prefix + "entry:" + id
}

func (s *Store) hashKey(namespace string, hash [32]byte) string {
	return s.prefix + "hash:" + namespace + ":" + hex.EncodeToString(hash[:])
}

func (s *Store) lineageKey(sourceID string) string {
	return s.prefix + "lineage:" + sourceID
}

func (s *Store) Get(ctx context.Context, id string) (*store.CacheEntry, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.store.get")
	defer span.End()
	span.SetAttributes(attribute.String("gen_ai.system", "reverb"), attribute.String("gen_ai.cache.store.backend", "redis"), attribute.String("gen_ai.cache.entry_id", id))

	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	data, err := s.client.HGet(ctx, s.entryKey(id), "data").Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, nil
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("redis HGet: %w", err)
	}
	var entry store.CacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, fmt.Errorf("unmarshal entry: %w", err)
	}
	return &entry, nil
}

func (s *Store) GetByHash(ctx context.Context, namespace string, hash [32]byte) (*store.CacheEntry, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.store.get_by_hash")
	defer span.End()
	span.SetAttributes(attribute.String("gen_ai.system", "reverb"), attribute.String("gen_ai.cache.store.backend", "redis"), attribute.String("gen_ai.cache.namespace", namespace))

	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	id, err := s.client.Get(ctx, s.hashKey(namespace, hash)).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("redis GET hash: %w", err)
	}
	return s.Get(ctx, id)
}

func (s *Store) Put(ctx context.Context, entry *store.CacheEntry) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.store.put")
	defer span.End()
	span.SetAttributes(attribute.String("gen_ai.system", "reverb"), attribute.String("gen_ai.cache.store.backend", "redis"), attribute.String("gen_ai.cache.entry_id", entry.ID), attribute.String("gen_ai.cache.namespace", entry.Namespace))

	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal entry: %w", err)
	}

	entryKey := s.entryKey(entry.ID)
	newHashKey := s.hashKey(entry.Namespace, entry.PromptHash)

	// Pre-read old entry to supply stale-index keys to the Lua script.
	// The script re-reads the entry atomically and performs all mutations
	// inside a single EVAL, so the write path is race-free.  This pre-read
	// is only used to compute which old Redis keys need cleaning; if it races
	// with another writer the script's own read will see the latest state and
	// any extra DEL/SREM on a missing key is harmless.
	var oldHashKey string
	var removeLineageKeys []string
	{
		raw, rerr := s.client.HGet(ctx, entryKey, "data").Bytes()
		if rerr == nil {
			var old store.CacheEntry
			if jerr := json.Unmarshal(raw, &old); jerr == nil {
				oldHashKey = s.hashKey(old.Namespace, old.PromptHash)
				for _, src := range old.SourceHashes {
					removeLineageKeys = append(removeLineageKeys, s.lineageKey(src.SourceID))
				}
			}
		}
		if oldHashKey == "" {
			oldHashKey = newHashKey // script skips DEL when keys match
		}
	}

	addLineageKeys := make([]string, 0, len(entry.SourceHashes))
	for _, src := range entry.SourceHashes {
		addLineageKeys = append(addLineageKeys, s.lineageKey(src.SourceID))
	}

	// ARGV layout matches putScript:
	//   [1] = oldHashKey
	//   [2] = entryJSON
	//   [3] = entryID
	//   [4] = removeCount
	//   [5..4+removeCount] = lineageKeysToRemove
	//   [4+removeCount+1] = addCount
	//   [4+removeCount+2..] = lineageKeysToAdd
	argv := make([]interface{}, 0, 4+len(removeLineageKeys)+1+len(addLineageKeys))
	argv = append(argv, oldHashKey)
	argv = append(argv, string(data))
	argv = append(argv, entry.ID)
	argv = append(argv, len(removeLineageKeys))
	for _, k := range removeLineageKeys {
		argv = append(argv, k)
	}
	argv = append(argv, len(addLineageKeys))
	for _, k := range addLineageKeys {
		argv = append(argv, k)
	}

	if err := putScript.Run(ctx, s.client, []string{entryKey, newHashKey}, argv...).Err(); err != nil {
		return fmt.Errorf("redis Put script: %w", err)
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.store.delete")
	defer span.End()
	span.SetAttributes(attribute.String("gen_ai.system", "reverb"), attribute.String("gen_ai.cache.store.backend", "redis"), attribute.String("gen_ai.cache.entry_id", id))

	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	entry, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if entry == nil {
		return nil
	}

	pipe := s.client.Pipeline()
	pipe.Del(ctx, s.entryKey(id))
	pipe.Del(ctx, s.hashKey(entry.Namespace, entry.PromptHash))
	for _, src := range entry.SourceHashes {
		pipe.SRem(ctx, s.lineageKey(src.SourceID), id)
	}
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("redis pipeline Delete: %w", err)
	}
	return nil
}

func (s *Store) DeleteBatch(ctx context.Context, ids []string) error {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "gen_ai.cache.store.delete_batch")
	defer span.End()
	span.SetAttributes(attribute.String("gen_ai.system", "reverb"), attribute.String("gen_ai.cache.store.backend", "redis"), attribute.Int("gen_ai.cache.batch_size", len(ids)))

	if err := ctx.Err(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	for _, id := range ids {
		if err := s.Delete(ctx, id); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return err
		}
	}
	return nil
}

func (s *Store) ListBySource(ctx context.Context, sourceID string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	members, err := s.client.SMembers(ctx, s.lineageKey(sourceID)).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return nil, nil
		}
		return nil, fmt.Errorf("redis SMEMBERS: %w", err)
	}
	if len(members) == 0 {
		return nil, nil
	}
	return members, nil
}

func (s *Store) IncrementHit(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	err := incrementHitScript.Run(ctx, s.client, []string{s.entryKey(id)}, now).Err()
	if err != nil && !errors.Is(err, goredis.Nil) {
		return fmt.Errorf("redis IncrementHit script: %w", err)
	}
	return nil
}

func (s *Store) Scan(ctx context.Context, namespace string, fn func(*store.CacheEntry) bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pattern := s.prefix + "entry:*"
	var cursor uint64
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		keys, next, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return fmt.Errorf("redis SCAN: %w", err)
		}
		for _, key := range keys {
			data, err := s.client.HGet(ctx, key, "data").Bytes()
			if err != nil {
				if errors.Is(err, goredis.Nil) {
					continue
				}
				return fmt.Errorf("redis HGet during scan: %w", err)
			}
			var entry store.CacheEntry
			if err := json.Unmarshal(data, &entry); err != nil {
				return fmt.Errorf("unmarshal during scan: %w", err)
			}
			if entry.Namespace != namespace {
				continue
			}
			if !fn(&entry) {
				return nil
			}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return nil
}

func (s *Store) Stats(ctx context.Context) (*store.StoreStats, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pattern := s.prefix + "entry:*"
	var total int64
	nsSet := make(map[string]struct{})
	var cursor uint64
	for {
		keys, next, err := s.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return nil, fmt.Errorf("redis SCAN stats: %w", err)
		}
		for _, key := range keys {
			data, err := s.client.HGet(ctx, key, "data").Bytes()
			if err != nil {
				if errors.Is(err, goredis.Nil) {
					continue
				}
				return nil, fmt.Errorf("redis HGet stats: %w", err)
			}
			var entry store.CacheEntry
			if err := json.Unmarshal(data, &entry); err != nil {
				return nil, err
			}
			total++
			nsSet[entry.Namespace] = struct{}{}
		}
		cursor = next
		if cursor == 0 {
			break
		}
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
	return s.client.Close()
}
