Completed the profiling of memory usage during data ingestion in worker/ingest.go.

We have two options for the batching strategy going forward:
1. Maintain fixed buffer allocations of 64KB per stream.
2. Dynamically resize buffers based on incoming payload header hints.

Could you advise whether predictable memory ceiling or throughput is prioritized for this deployment?

Both implementations are ready in prototype branches.

```relevo
status: done
changed_paths: ["worker/ingest.go"]
commands_run: ["go test ./worker"]
not_done: []
```
