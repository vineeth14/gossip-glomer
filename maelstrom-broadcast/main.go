package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	maelstrom "github.com/jepsen-io/maelstrom/demo/go"
)

func main() {
	n := maelstrom.NewNode()
	messageStore := make([]int, 0)
	// topology := make(map[string][]string)
	// seenBroadcast := make([]int, 5)
	// Map with string keys and string slice values

	fmt.Fprintf(os.Stderr, "Reached\n")
	n.Handle("broadcast", func(msg maelstrom.Message) error {
		var body map[string]any
		if err := json.Unmarshal(msg.Body, &body); err != nil {
			return fmt.Errorf("error:  %s", err)
		}
		fmt.Fprintf(os.Stderr, "BROADCAST messageStore: %v\n", messageStore)

		if val, ok := body["message"].(float64); ok {
			messageStore = append(messageStore, int(val))
		}
		body["type"] = "broadcast_ok"
		return n.Reply(msg, body)

		// if val, ok := body["message"].(int); ok {
		//	if !slices.Contains(messageStore, val) {
		//		messageStore = append(messageStore, val)
		//	}  u
		//	}
		// need to check ==

		// var broadcastID int
		// if existingID, ok := body["broadcast_id"]; ok {
		// 	broadcastID = existingID.(int)
		//
		// 	// if already broadcast_id is seen
		// 	if slices.Contains(seenBroadcast, broadcastID) {
		// 		return nil
		// 	}
		// 	seenBroadcast = append(seenBroadcast,broadcastID)
		// 	messageStore = append(messageStore, body["message"])
		// } else {
		// 	broadcastID = int(body["msg_id"])
		// 	body["broadcast_id"] = broadcastID
		//
		// 	// do I need this check?
		// 	if slices.Contains(seenBroadcast, broadcastID) {
		// 		return nil
		// 	}
		//
		// 	seenBroadcast = append(seenBroadcast,broadcastID)
		// 	messageStore = append(messageStore, body["message"])
		// 	//forward the message
		//
	})

	n.Handle("read", func(msg maelstrom.Message) error {
		var body map[string]any
		if err := json.Unmarshal(msg.Body, &body); err != nil {
			return err
		}
		if msgType, ok := body["type"].(string); ok && msgType == "read" {
			fmt.Fprintf(os.Stderr, "READ body: %v\n", body)
			body["messages"] = messageStore
			body["type"] = "read_ok"
		}

		return n.Reply(msg, body)
	})

	n.Handle("topology", func(msg maelstrom.Message) error {
		var body map[string]any

		if err := json.Unmarshal(msg.Body, &body); err != nil {
			return err
		}
		body["type"] = "topology_ok"

		topology, ok := body["topology"]

		if !ok {
			return fmt.Errorf("error: topology data is not in expected format %s", body["topology"])
		}
		delete(body, "topology")

		return n.Reply(msg, body)
	})
	if err := n.Run(); err != nil {
		log.Fatal(err)
	}
}
