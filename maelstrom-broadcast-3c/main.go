package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	maelstrom "github.com/jepsen-io/maelstrom/demo/go"
)

func gossip(message int, messageStore map[int]struct{}, topology map[string][]string, n *maelstrom.Node, retrySet map[string]retryMessage) {
	// if message has not been seen
	if _, ok := messageStore[message]; !ok {
		// store
		messageStore[message] = struct{}{}

		// forward to neighbors
		for _, nei := range topology[n.ID()] {
			uniqueID := fmt.Sprintf("%s_%d_%s", n.ID(), message, nei)
			retrySet[uniqueID] = retryMessage{
				"gossip",
				message,
			}
			// go func() {
			// for _, ok := retrySet[msgID]; ok; {
			// time.Sleep(time.Millisecond * 100)

			n.Send(nei, map[string]any{
				"type":      "gossip",
				"message":   message,
				"unique_id": uniqueID,
				"src":       n.ID(),
			})
			// }
			// }()
		}

	}
}

type retryMessage struct {
	Type    string
	message int
}

func main() {
	n := maelstrom.NewNode()

	// seenMessage := make(map[uniqueMsgID]struct{})
	messageStore := make(map[int]struct{})
	topology := make(map[string][]string)
	retrySet := make(map[string]retryMessage)
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
		delete(retrySet, uniqueID)
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
