package main

import (
	"context"
	"log"
	"sync"

	"therealbroker/api/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	conn, err := grpc.Dial("localhost:5052", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer conn.Close()

	client := proto.NewBrokerClient(conn)

	cnt := 0

	var wg sync.WaitGroup

	ctx := context.Background()

	for {

		if cnt > 10000 {
			wg.Wait()
			cnt = 0
		}

		wg.Add(1)
	
		go func(){
			defer wg.Done()

			cnt ++
			publishReq := &proto.PublishRequest{
				Subject:           "my-subject",
				Body:              []byte("Hello, gRPC!"),
				ExpirationSeconds: 60,
			}

			publishResp, err := client.Publish(ctx, publishReq)
			if err != nil {
				log.Printf("Publish request failed: %v", err)
			} else {
				log.Printf("Published message with ID: %d", publishResp.Id)
			}
		}()
	}
}
