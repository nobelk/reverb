# Basic Example

Demonstrates the core Reverb workflow using only in-process components (no external services required):

- Creates a client with the fake embedder, in-memory store, and flat vector index
- Stores four cache entries covering different support topics
- Performs exact-match lookups (same prompt text → "exact" tier hit)
- Shows a cache miss for an unrelated prompt
- Prints cache statistics (entries, hit counts, hit rate)
- Invalidates an entry by source ID and confirms the subsequent lookup is a miss

## Run locally

```
go run ./examples/basic
```

## Run with Docker

Build from the repo root (the Dockerfile uses `.` as context):

```
docker build -f examples/basic/Dockerfile -t reverb-basic-example .
docker run --rm reverb-basic-example
```
