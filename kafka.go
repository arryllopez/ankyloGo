package ankylogo

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"
)

type RateLimitEvent struct {
	IP         string `json:"ip"`
	Endpoint   string `json:"endpoint"`
	Action     string `json:"action"` // "ALLOWED", "DENIED_WINDOW", "DENIED_BUCKET", "DENIED_RISK"
	Timestamp  int64  `json:"timestamp"`
	UserAgent  string `json:"useragent"`
	StatusCode int    `json:"statuscode"`
}

type EventPublisher interface {
	Publish(event RateLimitEvent)
}

type KafkaPublisher struct {
	client *kgo.Client
	topic  string
}

func NewKafkaPublisher(client *kgo.Client, topic string) *KafkaPublisher {
	return &KafkaPublisher{
		client: client,
		topic:  topic,
	}
}

func (k *KafkaPublisher) Publish(event RateLimitEvent) {
	ctx := context.Background()
	// 1. Serialize the RateLimitEvent to JSON
	eventBytes, err := json.Marshal(event)
	if err != nil {
		fmt.Printf("failed to marshal RateLimitEvent: %v\n", err)
		return
	}

	// 2. Produce a kgo.Record to the topic
	record := &kgo.Record{
		Topic: k.topic,
		Value: eventBytes,
	}

	// 3. Call k.client.Produce() to send it to Kafka
	// Using a callback to handle asynchronous results
	k.client.Produce(ctx, record, func(r *kgo.Record, err error) {
		if err != nil {
			fmt.Printf("record had error: %v\n", err)
		}
	})
}
