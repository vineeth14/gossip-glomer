package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	maelstrom "github.com/jepsen-io/maelstrom/demo/go"
)

type retryMessage struct {
	Type    string
	message int
}

type safeRetrySet struct {
	mu    sync.Mutex
	items map[string]retryMessage
}

func (s *safeRetrySet) Add(id string, msg retryMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[id] = msg
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

func gossip(message int, messageStore map[int]struct{}, topology map[string][]string, n *maelstrom.Node, retrySet *safeRetrySet) {
	// if message has not been seen
	if _, ok := messageStore[message]; !ok {
		// store
		messageStore[message] = struct{}{}

		// forward to neighbors
		for _, nei := range topology[n.ID()] {
			uniqueID := fmt.Sprintf("%s_%d_%s", n.ID(), message, nei)
			retrySet.Add(uniqueID, retryMessage{
				"gossip",
				message,
			})
			// pass variables to prevent closure bug
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

	}
}

func main() {
	n := maelstrom.NewNode()

	// seenMessage := make(map[uniqueMsgID]struct{})
	messageStore := make(map[int]struct{})
	topology := make(map[string][]string)
	retrySet := &safeRetrySet{items: make(map[string]retryMessage)}
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

	// We need to write a new struct for handling the msg from gossip to get "src"
	// use n.Send to the src
	// make a unique id for retry set in gossip function
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

	n.Handle("gossip_ok", func(msg maelstrom.Message) error {
		var body map[string]any

		if err := json.Unmarshal(msg.Body, &body); err != nil {
			fmt.Fprintf(os.Stderr, "%s", err)
		}

		uniqueID := body["unique_id"].(string)
		retrySet.Remove(uniqueID)
		return nil
	})

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
	if err := n.Run(); err != nil {
		log.Fatal(err)
	}
}
