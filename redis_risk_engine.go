package ankylogo

import (
	"sync"

	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
)

type RedisRiskEngine struct {
	kafkaClient *kgo.Client
	redisClient *redis.Client
	notified    sync.Map
	riskEngineConfig
}
