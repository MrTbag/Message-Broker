package module

import (
	"context"
	"log"
	"sync"
	"therealbroker/pkg/broker"
	"time"

	"github.com/opentracing/opentracing-go"

	_ "github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"

	"github.com/gocql/gocql"
)

type Module struct {
	mu            sync.Mutex
	closed        bool
	subscriptions map[string][]chan broker.Message
	messages      map[string][]broker.Message
	timestamps    map[string][]time.Time
	db *pgxpool.Pool
	cassandraSession *gocql.Session
}

const createMessagesTableSQL = `
CREATE TABLE messages (
    id SERIAL PRIMARY KEY,
    subject TEXT,
    body TEXT,
    expiration INTEGER 
);
`


func NewModule() broker.Broker {
    dbConfig, err := pgxpool.ParseConfig("host=postgres user=youruser password=yourpassword dbname=yourdb sslmode=disable")
    if err != nil {
        log.Fatalf("Failed to parse PostgreSQL config: %v", err)
    }

    connPool, err := pgxpool.ConnectConfig(context.Background(), dbConfig)
    if err != nil {
        log.Fatalf("Failed to create PostgreSQL connection pool: %v", err)
    }

	_, err = connPool.Exec(context.Background(), createMessagesTableSQL)
    if err != nil {
        log.Fatalf("Failed to create messages table: %v", err)
    }

	clusterConfig := gocql.NewCluster("cassandra-node1", "cassandra-node2", "cassandra-node3")
    clusterConfig.Keyspace = "yourkeyspace"
    clusterConfig.Consistency = gocql.Quorum

    cassandraSession, err := clusterConfig.CreateSession()
    if err != nil {
        log.Fatalf("Failed to create Cassandra session: %v", err)
    }

    return &Module{
        subscriptions: make(map[string][]chan broker.Message),
        messages:      make(map[string][]broker.Message),
        timestamps:    make(map[string][]time.Time),
        db:            connPool,
		cassandraSession: cassandraSession,
    }
}



func (m *Module) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return broker.ErrUnavailable
	}

	m.closed = true
	return nil
}

func (m *Module) Publish(ctx context.Context, subject string, msg broker.Message) (int, error) {
	span, _ := opentracing.StartSpanFromContext(ctx, "Publish-Internal")
	defer span.Finish()

	spanLock, _ := opentracing.StartSpanFromContext(ctx, "Lock")

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return 0, broker.ErrUnavailable
	}

	spanLock.Finish()

	spanID, _ := opentracing.StartSpanFromContext(ctx, "ID")

	id := len(m.messages[subject]) + 1

	conn, err := m.db.Acquire(ctx)
    if err != nil {
        log.Printf("Failed to acquire PostgreSQL connection: %v", err)
        return 0, err
    }
    defer conn.Release()

    // Convert msg.Expiration to seconds
    expirationSeconds := int(msg.Expiration.Seconds())

    // Store the message in PostgreSQL using the acquired connection
    _, err = conn.Exec(ctx,
        "INSERT INTO messages (subject, body, expiration) VALUES ($1, $2, $3)",
        subject, msg.Body, expirationSeconds)
    if err != nil {
        log.Printf("Failed to store message in PostgreSQL: %v", err)
        return 0, err
    }

	// Store the message in Cassandra
	err = m.cassandraSession.Query(`
	INSERT INTO messages (subject, id, body, expiration)
	VALUES (?, ?, ?, ?)`,
	subject, id, msg.Body, msg.Expiration).Exec()
	if err != nil {
		log.Printf("Failed to store message in Cassandra: %v", err)
	}

	spanID.Finish()

	spanAppend, _ := opentracing.StartSpanFromContext(ctx, "Append")

	m.messages[subject] = append(m.messages[subject], msg)
	m.timestamps[subject] = append(m.timestamps[subject], time.Now()) // Store the timestamp

	spanAppend.Finish()

	spanChan, _ := opentracing.StartSpanFromContext(ctx, "Channel")

	for _, subChan := range m.subscriptions[subject] {
		select {
		case subChan <- msg:
		default:
		}
	}

	spanChan.Finish()

	return id, nil
}

func (m *Module) Subscribe(ctx context.Context, subject string) (<-chan broker.Message, error) {
	span, _ := opentracing.StartSpanFromContext(ctx, "Subscribe-Internal")
	defer span.Finish()

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil, broker.ErrUnavailable
	}

	_, err := m.db.Exec(ctx,
        "INSERT INTO subscriptions (subject) VALUES ($1)",
        subject)
    if err != nil {
        log.Printf("Failed to store subscription in PostgreSQL: %v", err)
    }

	subChan := make(chan broker.Message, 100)
	m.subscriptions[subject] = append(m.subscriptions[subject], subChan)

	return subChan, nil
}

// Fetch function
func (m *Module) Fetch(ctx context.Context, subject string, id int) (broker.Message, error) {
	span, _ := opentracing.StartSpanFromContext(ctx, "Fetch-Internal")
	defer span.Finish()

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return broker.Message{}, broker.ErrUnavailable
	}

	if id <= 0 || id > len(m.messages[subject]) {
		return m.fetchFromDatabase(ctx, subject, id)
	}

	msg := m.messages[subject][id-1]

	if msg.Expiration > 0 && time.Since(m.timestamps[subject][id-1]) > msg.Expiration {
		return broker.Message{}, broker.ErrExpiredID
	}

	return msg, nil
}

// fetchFromDatabase function
func (m *Module) fetchFromDatabase(ctx context.Context, subject string, id int) (broker.Message, error) {
	var body string
	var expiration time.Time

	// Fetch data from PostgreSQL using pgx.QueryRow
	err := m.db.QueryRow(ctx,
    	"SELECT body, expiration FROM messages WHERE subject = $1 AND id = $2",
        subject, id).Scan(&body, &expiration)
    if err != nil {
        return broker.Message{}, err
    }

	if expiration.After(time.Now()) {
		return broker.Message{
			Body:       body,
			Expiration: time.Until(expiration),
		}, nil
	}

	return broker.Message{}, broker.ErrExpiredID
}
