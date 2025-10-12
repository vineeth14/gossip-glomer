package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	maelstrom "github.com/jepsen-io/maelstrom/demo/go"
)

func gossip(message int, messageStore map[int]struct{}, topology map[string][]string, n *maelstrom.Node) {
	// if message has not been seen
	if _, ok := messageStore[message]; !ok {
		// store
		messageStore[message] = struct{}{}

		// forward to neighbors
		for _, nei := range topology[n.ID()] {
			n.Send(nei, map[string]any{
				"type":    "gossip",
				"message": message,
			})
		}

	}
}

func main() {
	n := maelstrom.NewNode()
	// seenMessage is a set

	// seenMessage := make(map[uniqueMsgID]struct{})
	messageStore := make(map[int]struct{})
	topology := make(map[string][]string)
	n.Handle("broadcast", func(msg maelstrom.Message) error {
		var body map[string]any

		if err := json.Unmarshal(msg.Body, &body); err != nil {
			fmt.Fprintf(os.Stderr, "%s", err)
		}

		message := int(body["message"].(float64))
		defer gossip(message, messageStore, topology, n)

		return n.Reply(msg, map[string]any{
			"type": "broadcast_ok",
		})
	})

	n.Handle("gossip", func(msg maelstrom.Message) error {
		var body map[string]any

		if err := json.Unmarshal(msg.Body, &body); err != nil {
			fmt.Fprintf(os.Stderr, "%s", err)
		}

		message := int(body["message"].(float64))

		gossip(message, messageStore, topology, n)
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
		return n.Reply(msg, body)
	})
	if err := n.Run(); err != nil {
		log.Fatal(err)
	}
}
