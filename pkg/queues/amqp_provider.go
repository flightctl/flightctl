package queues

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sirupsen/logrus"
)

type amqpProvider struct {
	url     string
	log     logrus.FieldLogger
	wg      *sync.WaitGroup
	queues  []*amqpQueue
	stopped atomic.Bool
	mu      sync.Mutex
}

func NewAmqpProvider(url string, log logrus.FieldLogger) Provider {
	var wg sync.WaitGroup
	wg.Add(1)
	return &amqpProvider{
		url: url,
		log: log,
		wg:  &wg,
	}
}

func (r *amqpProvider) newQueue(queueName string) (*amqpQueue, error) {
	var (
		err        error
		connection *amqp.Connection
		channel    *amqp.Channel
	)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped.Load() {
		return nil, errors.New("provider is stopped")
	}
	defer func() {
		if err != nil {
			if channel != nil {
				channel.Close()
			}
			if connection != nil {
				connection.Close()
			}
		}
	}()
	connection, err = amqp.Dial(r.url)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection: %w", err)
	}
	channel, err = connection.Channel()
	if err != nil {
		return nil, fmt.Errorf("failed to create channel: %w", err)
	}
	_, err = channel.QueueDeclare(queueName,
		true,  // durable
		false, // auto delete
		false, // exclusive
		false, // no wait
		nil)   // args

	if err != nil {
		return nil, fmt.Errorf("failed to declare queue %s: %w", queueName, err)
	}
	ret := &amqpQueue{
		name:       queueName,
		connection: connection,
		channel:    channel,
		log:        r.log,
		wg:         r.wg,
	}
	r.queues = append(r.queues, ret)
	return ret, nil
}

func (a *amqpProvider) NewConsumer(queueName string) (Consumer, error) {
	return a.newQueue(queueName)
}

func (a *amqpProvider) NewPublisher(queueName string) (Publisher, error) {
	return a.newQueue(queueName)
}

func (a *amqpProvider) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stopped.Swap(true) {
		return
	}
	defer a.wg.Done()
	for _, q := range a.queues {
		q.Close()
	}
}

func (a *amqpProvider) Wait() {
	a.wg.Wait()
}

type amqpQueue struct {
	connection *amqp.Connection
	channel    *amqp.Channel
	name       string
	wg         *sync.WaitGroup
	log        logrus.FieldLogger
	closed     atomic.Bool
}

func (r *amqpQueue) Publish(payload []byte) error {
	if r.closed.Load() {
		return errors.New("queue is closed")
	}
	return r.channel.Publish("",
		r.name, // queue name
		false,  // mandatory
		false,  // immediate
		amqp.Publishing{
			DeliveryMode: amqp.Persistent,
			ContentType:  "text/plain",
			Body:         payload,
		})
}

func (r *amqpQueue) Consume(ctx context.Context, handler ConsumeHandler) error {
	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	msgs, err := r.channel.ConsumeWithContext(ctx,
		r.name,
		"",    // consumer (exchange)
		false, // auto ack
		false, // exclusive
		false, // no local
		false, // no wait
		nil)   // args
	if err != nil {
		cancel()
		return err
	}
	r.wg.Add(1)
	go func() {
		var err error
		defer r.wg.Done()
		defer cancel()
		for d := range msgs {
			requestID := reqid.NextRequestID()
			reqCtx := context.WithValue(ctx, middleware.RequestIDKey, requestID)
			log := log.WithReqIDFromCtx(reqCtx, r.log)
			if err = handler(reqCtx, d.Body, log); err != nil {
				log.WithError(err).Errorf("failed to consume message: %s", string(d.Body))
			}
			if err = d.Ack(false); err != nil {
				log.WithError(err).Errorf("failed to acknowledge message")
			}
		}
		if !r.closed.Load() {
			r.log.Fatal("channel was closed by AMQP provider")
		}
	}()
	return nil
}

func (a *amqpQueue) Close() {
	if a.closed.Swap(true) {
		return
	}
	if a.channel != nil {
		a.channel.Close()
	}
	if a.connection != nil {
		a.connection.Close()
	}
}
