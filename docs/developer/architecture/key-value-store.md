# Key-Value Store Architecture

Flight Control uses Redis as a key-value store for two primary purposes: **caching external configuration data** and **managing an event-driven task queue**. This document describes both use cases and the resilience mechanisms that ensure system reliability.

## Overview

The key-value store serves as:
1. **Cache Layer**: Stores external configuration data (Git repositories, HTTP endpoints, Kubernetes secrets)
2. **Event Queue**: Manages asynchronous task processing through Redis Streams
3. **Resilience Backend**: Provides automatic recovery from failures

## Caching External Configuration Data

Flight Control caches external configuration sources to improve performance and reduce load on external systems. The cache is organized by organization, fleet, and template version to ensure proper isolation.

### Cached Data Types

| Data Type | Key Pattern | Description |
|-----------|-------------|-------------|
| **Git Repository URLs** | `v1/{orgId}/{fleet}/{templateVersion}/repo-url/{repository}` | Repository URL mappings |
| **Git Revisions** | `v1/{orgId}/{fleet}/{templateVersion}/git-hash/{repository}/{targetRevision}` | Git commit hashes for specific revisions |
| **Git File Contents** | `v1/{orgId}/{fleet}/{templateVersion}/git-data/{repository}/{targetRevision}/{path}` | Actual file contents from Git repositories |
| **Kubernetes Secrets** | `v1/{orgId}/{fleet}/{templateVersion}/k8ssecret-data/{namespace}/{name}` | Secret data from Kubernetes clusters |
| **HTTP Response Data** | `v1/{orgId}/{fleet}/{templateVersion}/http-data/{md5(url)}` | Content fetched from HTTP endpoints |

### Cache Behavior

- **Cache Keys**: Automatically scoped by organization, fleet, and template version
- **Cache Invalidation**: Keys are deleted when template versions change
- **Cache Miss Handling**: External sources are fetched on-demand when cache misses occur
- **Atomic Operations**: Uses a custom Lua script to implement get-or-set-if-not-exists behavior, preventing race conditions during concurrent access

### Important Cache Considerations

⚠️ **Cache Consistency Warning**: When external configuration sources change without updating their references (branch/tag/URL), devices may experience configuration drift:

- **Before cache deletion**: Some devices get `value1` from cache
- **After cache deletion**: Other devices get `value2` from fresh fetch
- **Result**: Inconsistent device configurations across the fleet

**Best Practice**: Always update branch names, tags, or URLs when changing external configuration content to ensure cache consistency.

## Event-Driven Task Queue

Flight Control uses Redis Streams with consumer groups to process events asynchronously. Events are published to the `task-queue` stream and processed by worker components.

### Event Processing Flow

```mermaid
flowchart TD
    A[API Event Created] --> B[Event Published to task-queue]
    B --> C[Worker Consumes Event]
    C --> D{Event Type Analysis}
    
    D --> E[Fleet Rollout Task]
    D --> F[Fleet Selector Matching Task]
    D --> G[Fleet Validation Task]
    D --> H[Device Render Task]
    D --> I[Repository Update Task]
    
    E --> J[Task Completion]
    F --> J
    G --> J
    H --> J
    I --> J
    
    J --> K[Event Acknowledged]
    K --> L[Checkpoint Advanced]
```

### Event-to-Task Mapping

| Task | Triggering Events | Description |
|------|------------------|-------------|
| **Fleet Rollout** | • Device owner/labels updated<br/>• Device created<br/>• Fleet rollout batch dispatched<br/>• Fleet rollout started (immediate strategy) | Manages device configuration updates according to fleet templates |
| **Fleet Selector Matching** | • Fleet label selector updated<br/>• Fleet created/deleted<br/>• Device created<br/>• Device labels updated | Matches devices to fleets based on label selectors |
| **Fleet Validation** | • Fleet template updated<br/>• Fleet created<br/>• Referenced repository updated | Validates fleet templates and creates template versions |
| **Device Render** | • Device spec updated<br/>• Device created<br/>• Fleet rollout device selected<br/>• Referenced repository updated | Renders device configurations from templates |
| **Repository Update** | • Repository spec updated<br/>• Repository created | Updates repository references and invalidates related caches |

### Queue Management Features

- **Consumer Groups**: Automatic message tracking and load balancing
- **Message Acknowledgment**: Messages are acknowledged after successful processing
- **Timeout Handling**: Messages that exceed processing timeout are automatically retried
- **Failed Message Handling**: Failures are retried with exponential backoff until a maximum number of retries, after which an event is emitted notifying about a permanent failure
- **Checkpoint Tracking**: Global checkpoint ensures no message loss during failures

## Resilience and Recovery

Flight Control implements a **dual-persistence architecture** to ensure no event loss during Redis failures (at-least-once delivery; duplicate processing possible):


### Architecture Components

1. **Redis Streams**: Primary queue for fast event processing
2. **PostgreSQL Database**: Persistent storage for events and checkpoints
3. **Recovery Mechanism**: Automatic event republishing from database

### Recovery Process

When Redis fails or is restarted:

1. **Checkpoint Detection**: System detects missing Redis checkpoint
2. **Database Checkpoint Retrieval**: Last known checkpoint is retrieved from PostgreSQL
3. **Event Republishing**: All events since the last checkpoint are republished to Redis
4. **Queue Restoration**: Fresh Redis instance receives all missed events
5. **Normal Operation**: Processing resumes; events since the checkpoint may be reprocessed. Handlers must be idempotent.

Note: The replay window equals “now - last persisted checkpoint”. Increase checkpoint persistence frequency to shorten replay/duplication.

### Recovery Limitations

⚠️ **Important**: The resilience mechanism only replays **events in the queue**. It does not restore cached external configuration data:

- **Events**: Automatically republished from PostgreSQL database
- **Cache Data**: Must be re-fetched from external sources (Git, HTTP, Kubernetes)
- **Cache Invalidation**: Occurs automatically when template versions change

## Redis Memory Configuration and Tuning

Flight Control uses Redis as an in-memory store, which requires careful memory management to prevent unbounded growth and ensure system stability.

### Memory Configuration Parameters

Redis memory usage is controlled by two key parameters:

| Parameter | Description | Default | Tuning Guidance |
|-----------|-------------|---------|-----------------|
| **maxmemory** | Total memory limit for Redis | `1gb` | Set to 70-80% of available container memory |
| **maxmemory-policy** | Eviction policy when limit reached | `allkeys-lru` | See policy recommendations below |

### Memory Eviction Policies

Choose the appropriate eviction policy based on your use case:

| Policy | Description | Use Case | Recommendation |
|--------|-------------|----------|----------------|
| **allkeys-lru** | Evict least recently used keys | General caching (default) | ✅ **Recommended** for most deployments |
| **allkeys-lfu** | Evict least frequently used keys | Long-running caches | Good for stable workloads |
| **volatile-lru** | Evict LRU keys with expiration | Mixed cache/queue data | Use if some keys have TTL |
| **noeviction** | Return errors when limit reached | Critical data preservation | ❌ **Not recommended** - causes failures |

### Memory Usage Patterns

Understanding Redis memory usage helps with proper sizing:

#### Cache Data (Primary Memory Consumer)
- **Git repository contents**: Large files, multiple versions
- **HTTP response data**: External API responses
- **Kubernetes secrets**: Configuration data
- **Template rendering results**: Processed configurations

#### Queue Data (Secondary Memory Consumer)
- **Task queue messages**: Event processing data
- **Failed message retry queue**: Exponential backoff storage
- **In-flight task tracking**: Processing state management

#### Podman Environment Variables
```bash
# Set environment variables before starting containers
export REDIS_MAXMEMORY="2gb"
export REDIS_MAXMEMORY_POLICY="allkeys-lru"
export REDIS_LOGLEVEL="warning"
```

### Memory Monitoring and Tuning

#### Key Metrics to Monitor
1. **Redis memory usage**: `redis-cli INFO memory`
2. **Evicted keys count**: `redis-cli INFO stats | grep evicted`
3. **Cache hit ratio**: Monitor cache effectiveness
4. **Queue depth**: Monitor task processing backlog

#### Tuning Guidelines

**Increase memory if:**
- High eviction rates (keys being removed frequently)
- Cache hit ratio below 80%
- Queue processing delays due to memory pressure

**Decrease memory if:**
- Memory usage consistently below 50%
- System has memory constraints
- Other services need more memory

#### Memory Calculation Formula
```
Recommended Redis Memory = 
  (Available Container Memory × 0.75) - 200MB
```

Where:
- `0.75` = 75% of container memory for Redis
- `200MB` = Buffer for Redis overhead and OS

### Configuration Examples

#### Helm Chart Configuration
```yaml
# values.yaml
kv:
  enabled: true
  maxmemory: "2gb"
  maxmemoryPolicy: "allkeys-lru"
  loglevel: "warning"
  resources:
    requests:
      memory: "2.5Gi"  # Container memory should be > maxmemory
      cpu: "1000m"
```

#### Podman Container Configuration
```ini
# flightctl-kv.container
[Container]
Environment=REDIS_MAXMEMORY=2gb
Environment=REDIS_MAXMEMORY_POLICY=allkeys-lru
Environment=REDIS_LOGLEVEL=warning
```

### Troubleshooting Memory Issues

#### Common Problems and Solutions

**Problem**: Redis running out of memory
```
Error: OOM command not allowed when used memory > 'maxmemory'
```
**Solution**: Increase `maxmemory` or improve eviction policy

**Problem**: High eviction rates
```
# Check eviction stats
redis-cli INFO stats | grep evicted
```
**Solution**: Increase memory allocation or optimize cache usage

**Problem**: Slow queue processing
**Solution**: Monitor queue depth and increase memory if needed