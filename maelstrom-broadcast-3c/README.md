# Maelstrom Broadcast 3c - Implementation Guide

## Overview

This implementation solves the Maelstrom broadcast challenge 3c, which requires building a fault-tolerant gossip-based broadcast system that continues working during network partitions. This guide explains how the solution works from both distributed systems and Go programming perspectives, and documents what was fixed from earlier iterations.

---

## How the Solution Works

### Distributed Systems Perspective

#### 1. Gossip Protocol for Message Propagation

**Core mechanism:** When a node receives a message, it forwards it to all its neighbors. Those neighbors forward to their neighbors, creating epidemic spread.

```
Client sends message 42 to n1
    ↓
n1 stores 42, gossips to neighbors [n2, n3]
    ↓
n2 receives 42, gossips to [n1, n4]
n3 receives 42, gossips to [n1, n4]
    ↓
n4 receives 42 from both n2 and n3
    ↓
Eventually all nodes have message 42
```

**Why this works:**

- **Multiple propagation paths**: Message 42 reaches n4 via both n2 AND n3 (redundancy)
- **Exponential spread**: Each hop potentially doubles the nodes that know about the message
- **Decentralized**: No single point of failure or coordination

#### 2. Handling Network Partitions with Retries

**The challenge:** Network failures can split your cluster into isolated islands.

```
Before partition:        During partition:
n1 ---- n2              n1  X  n2
 |       |               |      |
n3 ---- n4              n3  X  n4

All connected           Left: [n1, n3]
                        Right: [n2, n4]
```

**What happens without retries:**

1. Client sends message 42 to n1 (left island)
2. n1 gossips to n3 ✅ (same island)
3. n1 tries to gossip to n2 ❌ (network broken)
4. Message 42 never reaches n2 or n4

**Our retry mechanism (maelstrom-broadcast-3c/main.go:57-70):**

```go
go func(neighbor string, id string, msg int) {
    for {
        if !retrySet.Exists(id) {
            break
        }
        time.Sleep(time.Millisecond * 100)
        n.Send(neighbor, map[string]any{
            "type":      "gossip",
            "message":   msg,
            "unique_id": id,
            "src":       n.ID(),
        })
    }
}(nei, uniqueID, message)
```

**How this solves partitions:**

1. n1 spawns a goroutine that keeps retrying message 42 to n2 every 100ms
2. During partition: All sends fail silently
3. **When partition heals**: One of the retries succeeds!
4. n2 sends `gossip_ok` acknowledgment
5. n1 removes from `retrySet`, goroutine exits
6. n2 gossips to n4, completing propagation

**Key insight:** Retries convert temporary network failures into eventual success, achieving partition tolerance.

#### 3. Deduplication to Prevent Infinite Loops

**The problem without deduplication:**

```
n1 sends 42 to n2
    ↓
n2 sends 42 to n1 (n1 is a neighbor)
    ↓
n1 sends 42 to n2 (n2 is a neighbor)
    ↓
∞ INFINITE LOOP ∞
```

**The solution (maelstrom-broadcast-3c/main.go:45-47):**

```go
if _, ok := messageStore[message]; !ok {
    messageStore[message] = struct{}{}
    // Forward to neighbors
}
```

**How it works:**

- Each node maintains a set of messages it has seen
- Before processing a message, check if it's already in the set
- If seen before, ignore it
- If new, add to set and forward to neighbors

**Example:**

```
n1 receives 42 → messageStore[42] = ✓ → gossip to [n2, n3]
n1 receives 42 again from n2 → already in messageStore → ignore
```

This prevents both infinite loops and duplicate processing.

#### 4. At-Least-Once Delivery with Idempotency

**Our delivery guarantee:** Messages may arrive multiple times but WILL arrive at least once.

**Scenario:**

```
n1 sends gossip(42) to n2
    ↓
n2 receives it, stores it
    ↓
n2 sends gossip_ok back
    ↓
gossip_ok lost in network ❌
    ↓
n1 never receives ack, keeps retrying
    ↓
n2 receives gossip(42) AGAIN
```

**Handling duplicates:** Deduplication makes processing idempotent:

```go
// Second time n2 receives message 42:
if _, ok := messageStore[42]; !ok {
    // Won't enter - 42 already exists
}
// Duplicate ignored, no reprocessing
```

**This is idempotency:** Processing the same message multiple times has the same effect as processing it once.

#### 5. Acknowledgment-Based Retry Termination

**How retry goroutines know when to stop:**

```
n1                                    n2
 |                                     |
 |---- gossip(msg=42, uid="n1_42_n2") -->|
 |                                     | Store 42
 |<-------- gossip_ok(uid="n1_42_n2") ---|
 | Remove "n1_42_n2" from retrySet    |
 | Goroutine checks: !Exists("n1_42_n2")|
 | Returns true → break → exit ✓      |
```

**Why unique IDs matter:** Each message-to-neighbor pair needs separate tracking.

Example for n1 sending message 42 to 3 neighbors:

- To n2: `"n1_42_n2"` (might fail, keep retrying)
- To n3: `"n1_42_n3"` (succeeds, stop retrying)
- To n4: `"n1_42_n4"` (might fail, keep retrying)

These are independent transmissions. Some might succeed while others fail due to network issues.

#### 6. Eventual Consistency

**Definition:** All nodes will eventually agree on the same set of messages, given enough time without failures.

**How we achieve it:**

1. **Gossip propagation**: Creates multiple redundant paths
2. **Retries**: Handle transient failures and partitions
3. **Deduplication**: Prevents message multiplication
4. **Acknowledgments**: Enable efficient retry termination

**Example timeline:**

```
T=0.0s: n1 has [42]
T=0.1s: n2, n3 receive from n1 → have [42]
T=0.2s: n4 receives from n2 and n3 → has [42]
T=0.3s: All nodes have [42] ✓
```

Even if some transmissions fail, retries ensure eventual delivery.

---

### Go Programming Perspective

#### 1. Goroutines for Concurrent Retry Loops

**What they enable:**

```go
for _, nei := range topology[n.ID()] {
    go func(neighbor string, id string, msg int) {
        // Each neighbor gets its own retry loop
        for {
            if !retrySet.Exists(id) { break }
            time.Sleep(time.Millisecond * 100)
            n.Send(neighbor, ...)
        }
    }(nei, uniqueID, message)
}
```

**Goroutine properties:**

- **Lightweight**: Uses ~2-4KB stack (thousands can run simultaneously)
- **Concurrent**: All retry loops run in parallel
- **Independent**: If n2 is unreachable, n3's goroutine still succeeds

**Benefits for distributed systems:**

- Each message-to-neighbor transmission retries independently
- Partial network failures don't block successful transmissions
- Natural expression of concurrent retry logic

**Example:** Sending message 42 to 3 neighbors spawns 3 goroutines:

```
Main thread                  Goroutine 1         Goroutine 2         Goroutine 3
-----------                  -----------         -----------         -----------
spawn 3 goroutines
continue handling            retry → n2          retry → n3          retry → n4
other messages              retry → n2          SUCCESS ✓           retry → n4
                            retry → n2          (exits)             retry → n4
                            SUCCESS ✓                               retry → n4
                            (exits)                                 SUCCESS ✓
                                                                    (exits)
```

Each operates independently without blocking others.

#### 2. Mutexes for Thread-Safe Map Access

**The problem:** Multiple goroutines accessing the same map

**Concurrent access points in our code:**

- Gossip function writes to `retrySet` (main.go:52)
- Retry goroutines read from `retrySet` (main.go:59)
- `gossip_ok` handler deletes from `retrySet` (main.go:133)

**Without protection:** Race condition → crash

```
fatal error: concurrent map read and map write
```

**The solution (main.go:19-41):**

```go
type safeRetrySet struct {
    mu    sync.Mutex
    items map[string]retryMessage
}

func (s *safeRetrySet) Add(id string, msg retryMessage) {
    s.mu.Lock()         // Acquire exclusive access
    defer s.mu.Unlock() // Release when function exits
    s.items[id] = msg   // Safe - only one goroutine here
}

func (s *safeRetrySet) Exists(id string) bool {
    s.mu.Lock()
    defer s.mu.Unlock()
    _, ok := s.items[id]
    return ok
}

func (s *safeRetrySet) Remove(id string) {
    s.mu.Lock()
    defer s.mu.Unlock()
    delete(s.items, id)
}
```

**How mutexes work:**

```
Time    Goroutine 1          Goroutine 2          Goroutine 3
----    -----------          -----------          -----------
T1      Lock() → acquired    Lock() → waiting     Lock() → waiting
T2      items[x] = y         [blocked]            [blocked]
T3      Unlock()             Lock() → acquired    [blocked]
T4                           delete(items, z)     [blocked]
T5                           Unlock()             Lock() → acquired
T6                                                _, ok := items[w]
T7                                                Unlock()
```

Only one goroutine can hold the lock at a time. Others wait in a queue.

**Why `defer unlock`:**

- Guarantees unlock happens even if function panics
- Prevents deadlocks where all goroutines wait forever
- Idiomatic Go pattern for resource cleanup

#### 3. Avoiding the Closure Bug

**The closure bug:** When goroutines capture loop variables

**Wrong approach:**

```go
for _, nei := range neighbors {
    go func() {
        send(nei)  // ❌ All goroutines share the same 'nei'
    }()
}
```

**What happens:**

```
Loop iteration 1: nei = "n2", spawn goroutine G1
Loop iteration 2: nei = "n3", spawn goroutine G2
Loop iteration 3: nei = "n4", spawn goroutine G3
Loop completes: nei = "n4"

Goroutines start running:
G1: reads nei → "n4" ❌
G2: reads nei → "n4" ❌
G3: reads nei → "n4" ✓
```

All goroutines see the final loop value!

**Our fix (main.go:57):**

```go
go func(neighbor string, id string, msg int) {
    // neighbor, id, msg are NEW variables
    // Each goroutine gets its own copy
    n.Send(neighbor, map[string]any{
        "message":   msg,
        "unique_id": id,
    })
}(nei, uniqueID, message)  // Pass as arguments
```

**Why this works:**

- Parameters create copies for each goroutine
- Each goroutine has independent values
- No shared state between goroutines

**Distributed systems impact:** Without this fix, all retry goroutines might send to the same (last) neighbor, breaking message propagation!

#### 4. For Loop Mechanics: Init-Condition-Post

**Go for loop structure:**

```go
for init; condition; post {
    body
}

// Executes as:
init
while (condition) {
    body
    post
}
```

**Our retry loop (main.go:58-69):**

```go
for {
    if !retrySet.Exists(id) {
        break
    }
    time.Sleep(time.Millisecond * 100)
    n.Send(neighbor, ...)
}
```

**Why we use `for { ... break ... }`:**

- Need to re-check the map on EVERY iteration
- `for init; condition; post` only executes init ONCE
- Our condition needs fresh data each time

**Execution flow:**

```
Iteration 1: Check retrySet.Exists(id) → true → send → sleep
... gossip_ok handler runs: retrySet.Remove(id)
Iteration 2: Check retrySet.Exists(id) → false → break → exit
```

Each iteration performs a fresh check.

#### 5. Empty Struct for Zero-Byte Sets

**The pattern (main.go:47, 80):**

```go
messageStore := make(map[int]struct{})
messageStore[42] = struct{}{}

if _, ok := messageStore[42]; ok {
    // Message 42 exists in set
}
```

**Why `struct{}` instead of `bool`?**

```go
// Option 1: Using bool
messageStore := make(map[int]bool)
messageStore[42] = true
// Each entry uses 1 byte for value

// Option 2: Using empty struct
messageStore := make(map[int]struct{})
messageStore[42] = struct{}{}
// Each entry uses 0 bytes for value ✓
```

**Memory savings:** For 1 million messages:

- `map[int]bool`: ~1 MB for values
- `map[int]struct{}`: 0 bytes for values

**We only care about key existence, not values.** Empty struct is perfect for implementing sets.

#### 6. Defer for Guaranteed Cleanup

**Basic behavior:**

```go
func example() {
    defer fmt.Println("Last")
    fmt.Println("First")
    fmt.Println("Second")
}
// Output: First, Second, Last
```

**Critical for mutex unlock:**

```go
func (s *safeRetrySet) Add(id string, msg retryMessage) {
    s.mu.Lock()
    defer s.mu.Unlock()  // Guaranteed to run

    s.items[id] = msg
    // Even if panic happens, unlock still executes
}
```

**Without defer:**

```go
s.mu.Lock()
s.items[id] = msg
// If panic occurs here, Unlock never runs → DEADLOCK
s.mu.Unlock()
```

**Why it matters:**

- Ensures locks always released
- Prevents deadlocks
- Makes code more maintainable (all cleanup in one place)

#### 7. The Comma-OK Idiom

**Pattern:**

```go
value, ok := myMap[key]
if ok {
    // Key exists, use value
} else {
    // Key doesn't exist
}
```

**Our usage (main.go:45):**

```go
if _, ok := messageStore[message]; !ok {
    // Message NOT seen before
    messageStore[message] = struct{}{}
    // Process and forward
}
```

**Breakdown:**

- `messageStore[message]`: Returns (value, exists)
- `_`: Discard the value (we don't need it)
- `ok`: Boolean - true if key exists
- `!ok`: True if key does NOT exist (new message)

This is idiomatic Go for checking map key existence.

---

## Complete Code Walkthrough

### Thread-Safe Retry Set (main.go:14-41)

```go
type retryMessage struct {
    Type    string
    message int
}

type safeRetrySet struct {
    mu    sync.Mutex
    items map[string]retryMessage
}
```

**Purpose:** Track which messages need retrying to which neighbors, with thread-safe access.

**Structure:**

- `retryMessage`: Metadata about each pending message
- `safeRetrySet.mu`: Mutex protecting concurrent access
- `safeRetrySet.items`: Map from uniqueID → message data

**Methods provide thread-safe operations:**

**Add (main.go:24-28):**

- **Distributed systems**: Register a new pending transmission
- **Go**: Acquire lock, write to map, release lock

**Exists (main.go:30-35):**

- **Distributed systems**: Check if still waiting for acknowledgment
- **Go**: Acquire lock, read from map, release lock

**Remove (main.go:37-41):**

- **Distributed systems**: Acknowledgment received, stop retrying
- **Go**: Acquire lock, delete from map, release lock

### The Gossip Function (main.go:43-74)

```go
func gossip(message int, messageStore map[int]struct{}, topology map[string][]string, n *maelstrom.Node, retrySet *safeRetrySet)
```

**Purpose:** Propagate a message to all neighbors with automatic retry.

**Deduplication check (main.go:45-47):**

```go
if _, ok := messageStore[message]; !ok {
    messageStore[message] = struct{}{}
```

- **Distributed systems**: Only process new messages
- **Go**: Comma-ok idiom for map existence check

**Neighbor iteration (main.go:50-71):**

```go
for _, nei := range topology[n.ID()] {
    uniqueID := fmt.Sprintf("%s_%d_%s", n.ID(), message, nei)
    retrySet.Add(uniqueID, retryMessage{"gossip", message})

    go func(neighbor string, id string, msg int) {
        for {
            if !retrySet.Exists(id) {
                break
            }
            time.Sleep(time.Millisecond * 100)
            n.Send(neighbor, map[string]any{
                "type":      "gossip",
                "message":   msg,
                "unique_id": id,
                "src":       n.ID(),
            })
        }
    }(nei, uniqueID, message)
}
```

**Flow:**

1. Generate unique ID for this sender-message-recipient triple
2. Add to retry set
3. Spawn goroutine that:
   - Checks if still in retry set
   - If yes, sends message and sleeps 100ms
   - Repeats until removed from retry set (acknowledged)

**Distributed systems concepts:**

- Topology-aware: Only send to direct neighbors
- Independent retries: Each neighbor has its own goroutine
- Partition tolerance: Keep retrying until success

**Go concepts:**

- Goroutine for concurrency
- Parameters passed to avoid closure bug
- Infinite loop with exit condition
- Thread-safe map operations

### Broadcast Handler (main.go:83-102)

```go
n.Handle("broadcast", func(msg maelstrom.Message) error {
    var body map[string]any
    if err := json.Unmarshal(msg.Body, &body); err != nil {
        fmt.Fprintf(os.Stderr, "%s", err)
    }

    message := int(body["message"].(float64))
    defer gossip(message, messageStore, topology, n, retrySet)

    err := n.Reply(msg, map[string]any{
        "type": "broadcast_ok",
    })
    for err != nil {
        err = n.Reply(msg, map[string]any{
            "type": "broadcast_ok",
        })
    }
    return nil
})
```

**Purpose:** Handle messages from clients (not other nodes).

**Flow:**

1. Parse JSON message body
2. Extract message value (type assertion from float64)
3. Schedule gossip with `defer` (runs after reply)
4. Reply to client immediately
5. Retry reply until successful
6. Return (deferred gossip executes now)

**Distributed systems:**

- Client gets low-latency response (reply before gossip)
- Reliable client communication (retry until success)
- Asynchronous propagation (gossip after reply)

**Go:**

- `defer`: Run gossip after function returns
- Type assertion: `body["message"].(float64)` (JSON numbers are float64)
- Retry loop: Keep trying until `err == nil`

### Gossip Handler (main.go:107-123)

```go
n.Handle("gossip", func(msg maelstrom.Message) error {
    var body map[string]any
    if err := json.Unmarshal(msg.Body, &body); err != nil {
        fmt.Fprintf(os.Stderr, "%s", err)
    }

    message := int(body["message"].(float64))
    dest := body["src"].(string)
    uniqueID := body["unique_id"].(string)

    gossip(message, messageStore, topology, n, retrySet)

    return n.Send(dest, map[string]any{
        "type":      "gossip_ok",
        "unique_id": uniqueID,
    })
})
```

**Purpose:** Handle gossip messages from other nodes.

**Flow:**

1. Parse message: data, sender, unique ID
2. Call `gossip()` to propagate to our neighbors
3. Send acknowledgment back to sender

**Distributed systems:**

- **Epidemic spread**: Receiving triggers re-gossip to neighbors
- **Acknowledgment**: Sender can stop retrying
- **Unique ID**: Sender matches ack to correct retry goroutine

**Why this creates exponential spread:**

```
n1 gossips to n2
    ↓
n2's gossip handler calls gossip()
    ↓
n2 gossips to [n3, n4]
    ↓
n3's and n4's handlers call gossip()
    ↓
Exponential propagation!
```

### Gossip OK Handler (main.go:125-135)

```go
n.Handle("gossip_ok", func(msg maelstrom.Message) error {
    var body map[string]any
    if err := json.Unmarshal(msg.Body, &body); err != nil {
        fmt.Fprintf(os.Stderr, "%s", err)
    }

    uniqueID := body["unique_id"].(string)
    retrySet.Remove(uniqueID)
    return nil
})
```

**Purpose:** Handle acknowledgments from neighbors.

**Flow:**

1. Extract unique ID from acknowledgment
2. Remove from retry set
3. Retry goroutine's next iteration sees `!retrySet.Exists(id)` → exits

**Complete lifecycle:**

```
gossip() creates entry in retrySet
    ↓
Spawns retry goroutine
    ↓
Goroutine sends message every 100ms
    ↓
Neighbor receives, sends gossip_ok
    ↓
gossip_ok handler removes from retrySet
    ↓
Retry goroutine checks, sees removed, exits
```

**Distributed systems:** Acknowledgment-based termination enables efficient retry without wasting resources.

### Topology Handler (main.go:137-148)

```go
n.Handle("topology", func(msg maelstrom.Message) error {
    var body struct {
        Type     string              `json:"type"`
        Topology map[string][]string `json:"topology"`
    }

    if err := json.Unmarshal(msg.Body, &body); err != nil {
        return err
    }

    topology = body.Topology

    return n.Reply(msg, map[string]string{"type": "topology_ok"})
})
```

**Purpose:** Receive network topology from Maelstrom.

**Distributed systems:**

- Defines neighbor relationships
- Reduces message overhead (only gossip to neighbors, not everyone)
- Creates structured overlay network

**Go:**

- Struct with JSON tags for automatic unmarshaling
- Updates shared topology map

### Read Handler (main.go:150-170)

```go
n.Handle("read", func(msg maelstrom.Message) error {
    var body map[string]any
    if err := json.Unmarshal(msg.Body, &body); err != nil {
        return err
    }

    messages := make([]int, 0, len(messageStore))
    for msg := range messageStore {
        messages = append(messages, msg)
    }

    if msgType, ok := body["type"].(string); ok && msgType == "read" {
        body["messages"] = messages
        body["type"] = "read_ok"
    }

    err := n.Reply(msg, body)
    for err != nil {
        err = n.Reply(msg, body)
    }

    return nil
})
```

**Purpose:** Return all messages this node knows about.

**Distributed systems:**

- Maelstrom uses this to verify eventual consistency
- All nodes should eventually return the same set of messages

**Go:**

- Convert map keys (set) to slice (array)
- Range over map to iterate keys
- Retry reply until success

---

## What Was Wrong Previously

### Issue 1: For Loop Init Ran Only Once

**Previous code:**

```go
for _, ok := retrySet[uniqueID]; ok; {
    time.Sleep(time.Millisecond * 100)
    n.Send(neighbor, ...)
}
```

**The problem:**

- `for init; condition; post` executes init ONCE at start
- `_, ok := retrySet[uniqueID]` ran once, setting `ok` to true
- Loop then checked that same `ok` variable forever
- Map never checked again, even after `delete(retrySet, uniqueID)`

**Execution:**

```
T=0: init → _, ok := retrySet[uid] → ok = true
T=1: condition → ok == true → enter loop
T=2: send message, sleep 100ms
... gossip_ok handler: delete(retrySet, uid)
T=3: condition → ok == true (still!) → continue loop
T=4: Loop continues forever ∞
```

**Impact:**

- Retry goroutines never stopped
- Wasted CPU and memory
- Messages sent infinitely (though recipient deduplication handled it)

**The fix:**

```go
for {
    if !retrySet.Exists(id) {  // Fresh check every iteration
        break
    }
    send message
}
```

### Issue 2: Concurrent Map Access

**Previous code had unsynchronized access:**

```go
// Line 22:
retrySet[uniqueID] = retryMessage{...}    // WRITE

// Line 27:
for _, ok := retrySet[uniqueID]; ok; {    // READ

// Line 105:
delete(retrySet, uniqueID)                // DELETE
```

**The problem:**

- Go maps are NOT thread-safe
- Multiple goroutines accessing simultaneously → panic
- `fatal error: concurrent map read and map write`

**Timing example:**

```
T1: Goroutine 1: retrySet[id1] = msg1     (write)
T2: Goroutine 2: retrySet[id2] = msg2     (concurrent write) → CRASH
```

**Impact:**

- Program crashed randomly during high concurrency
- Higher message rates → more crashes
- Network partitions healing → burst of operations → crash

**The fix:**

```go
type safeRetrySet struct {
    mu    sync.Mutex
    items map[string]retryMessage
}

func (s *safeRetrySet) Add(id string, msg retryMessage) {
    s.mu.Lock()         // Only one goroutine at a time
    defer s.mu.Unlock()
    s.items[id] = msg
}
```

### Issue 3: Goroutine Closure Captured Loop Variables

**Previous code:**

```go
for _, nei := range topology[n.ID()] {
    uniqueID := fmt.Sprintf("%s_%d_%s", n.ID(), message, nei)
    go func() {
        n.Send(nei, map[string]any{    // ❌ Captures 'nei'
            "unique_id": uniqueID,      // ❌ Captures 'uniqueID'
        })
    }()
}
```

**The problem:**

- All goroutines shared the same `nei` and `uniqueID` variables
- By the time goroutines ran, loop had finished
- All goroutines saw the final loop values

**Execution:**

```
Loop iteration 1: nei="n2", uniqueID="n1_42_n2" → spawn G1
Loop iteration 2: nei="n3", uniqueID="n1_42_n3" → spawn G2
Loop iteration 3: nei="n4", uniqueID="n1_42_n4" → spawn G3
Loop completes: nei="n4", uniqueID="n1_42_n4"

Goroutines start:
G1: reads nei → "n4", uniqueID → "n1_42_n4" ❌
G2: reads nei → "n4", uniqueID → "n1_42_n4" ❌
G3: reads nei → "n4", uniqueID → "n1_42_n4" ✓

All send to n4 with same ID!
```

**Impact:**

- All goroutines sent to wrong neighbor (last in loop)
- Unique IDs were duplicated
- Acknowledgments couldn't match to correct goroutines
- Messages lost or incorrectly propagated

**The fix:**

```go
go func(neighbor string, id string, msg int) {
    // neighbor, id, msg are copies for this goroutine
    n.Send(neighbor, map[string]any{
        "unique_id": id,
    })
}(nei, uniqueID, message)  // Pass as arguments
```

### Issue 4: Missing Return Statement

**Previous code:**

```go
n.Handle("gossip_ok", func(msg maelstrom.Message) error {
    // ... handler code ...
    retrySet.Remove(uniqueID)
    // Missing: return nil
    // Missing: })

n.Handle("topology", func(msg maelstrom.Message) error {
```

**The problem:**

- `gossip_ok` handler missing closing brace
- `topology` handler registration was INSIDE `gossip_ok` handler

**What happened:**

- Every `gossip_ok` message registered a new `topology` handler
- After 100 acknowledgments: 100 topology handlers registered
- All 100 ran on every topology message
- Memory leak and performance degradation

**Impact:**

- Growing memory usage
- Slower topology updates
- Unpredictable behavior

**The fix:**

```go
n.Handle("gossip_ok", func(msg maelstrom.Message) error {
    // ... handler code ...
    retrySet.Remove(uniqueID)
    return nil     // ✓ Proper return
})                 // ✓ Close handler

n.Handle("topology", func(msg maelstrom.Message) error {
```

---

## Key Takeaways

### Distributed Systems Principles

1. **Gossip protocols** achieve eventual consistency through epidemic spread with multiple redundant paths
2. **Retries** convert transient failures into eventual success, providing partition tolerance
3. **Deduplication** prevents infinite loops and enables idempotent processing
4. **At-least-once delivery** is simpler than exactly-once and sufficient when combined with idempotency
5. **Acknowledgments** enable efficient retry termination without wasting resources
6. **Topology-aware propagation** reduces network overhead while maintaining reachability

### Go Programming Principles

1. **Goroutines** enable independent concurrent operations (retry loops per message-neighbor pair)
2. **Mutexes** protect shared data structures from concurrent access race conditions
3. **Defer** guarantees cleanup (especially unlocking) even during panics
4. **Closure bugs** avoided by passing loop variables as goroutine parameters
5. **Empty structs** create memory-efficient sets (zero bytes per value)
6. **For loop mechanics** require understanding init-condition-post execution order
7. **Comma-ok idiom** provides clean map existence checks

### The Synergy

Go's concurrency primitives perfectly match distributed systems needs:

- **Goroutines** = independent agents (retry loops, message handlers)
- **Mutexes/Channels** = coordination mechanisms (shared state protection)
- **Lightweight** = thousands of concurrent operations (message propagation)
- **Simple** = complex distributed logic expressed in readable code

This implementation demonstrates how Go features enable clean expression of distributed systems concepts like gossip protocols, retries, idempotency, and eventual consistency.
