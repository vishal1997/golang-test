package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
)

// Lease represents the structure of the lease document in the lease container
type Lease struct {
	ID                string `json:"id"`                // Partition key or range ID
	PartitionKey      string `json:"partitionKey"`      // Partition key for the lease container
	ContinuationToken string `json:"continuationToken"` // Current LSN for this partition
}

type Change map[string]interface{}

func getLatestLSN2(container *azcosmos.ContainerClient) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	//continuationToken := "" // Use an empty token to start from the beginning

	// Read the Change Feed
	enableCrossPartitionQuery := true
	options := &azcosmos.QueryOptions{
		ContinuationToken:         nil, // Start with nil
		PageSizeHint:              1,
		EnableCrossPartitionQuery: &enableCrossPartitionQuery,
	}
	pager := container.NewQueryItemsPager("SELECT * FROM c", azcosmos.NewPartitionKey(), options)
	var lsn int64 = 0
	for pager.More() {
		resp, err := pager.NextPage(ctx)
		if err != nil {
			log.Fatal("Error querying Change Feed:", err)
		}

		// Print all response headers (Check if `_lsn` exists)
		for key, value := range resp.RawResponse.Header {
			fmt.Printf("Header: %s = %s\n", key, value)
		}

		// If `_lsn` is included in headers, extract it
		lsn := resp.RawResponse.Header.Get("lsn")
		fmt.Println("LSN Value:", lsn)
	}
	return lsn, nil
}

// Fetch the current LSN from the lease container
func getCurrentLSN(leaseContainer *azcosmos.ContainerClient, partitionKey string) (int64, error) {
	log.Printf("Fetching current LSN for partition key: %s", partitionKey)
	ctx := context.TODO()
	query := fmt.Sprintf("SELECT c.ContinuationToken FROM c ")
	enableCrossPartitionQuery := true
	options := &azcosmos.QueryOptions{
		ContinuationToken:         nil, // Start with nil
		PageSizeHint:              1,
		EnableCrossPartitionQuery: &enableCrossPartitionQuery,
	}

	iterator := leaseContainer.NewQueryItemsPager(query, azcosmos.NewPartitionKey(), options)

	var continuationToken string
	for iterator.More() {
		resp, err := iterator.NextPage(ctx)
		if err != nil {
			log.Printf("Error while fetching continuation token: %v", err)
			return 0, fmt.Errorf("failed to fetch continuation token: %v", err)
		}
		log.Printf("Received response with %d items, %s items", len(resp.Items), resp.Items)

		if len(resp.Items) > 0 {
			var lease map[string]interface{}
			if err := json.Unmarshal(resp.Items[0], &lease); err != nil {
				log.Printf("Error unmarshaling lease item: %v", err)
				return 0, fmt.Errorf("failed to unmarshal lease item: %v", err)
			}
			continuationToken = lease["ContinuationToken"].(string)
		}
	}

	// Extract the LSN from the continuation token (assuming token contains the LSN as an integer)
	var currentLSN int64
	if _, err := fmt.Sscanf(continuationToken, "%d", &currentLSN); err != nil {
		log.Printf("Error parsing continuation token: %v", err)
		return 0, fmt.Errorf("failed to parse continuation token: %v", err)
	}

	log.Printf("Fetched current LSN: %d for partition key: %s", currentLSN, partitionKey)
	return currentLSN, nil
}

// Calculate the estimated lag for a partition
func calculateLag(container *azcosmos.ContainerClient, leaseContainer *azcosmos.ContainerClient, partitionKey string) (int64, error) {
	log.Printf("Calculating lag for partition key: %s", partitionKey)

	// Get the latest LSN for the partition
	latestLSN, err := getLatestLSN2(container) //getLatestLSN(container, partitionKey)
	if err != nil {
		log.Printf("Error getting latest LSN: %v", err)
		return 0, fmt.Errorf("failed to get latest LSN for partition %s: %v", partitionKey, err)
	}

	// Get the current LSN for the partition
	currentLSN, err := getCurrentLSN(leaseContainer, partitionKey)
	if err != nil {
		log.Printf("Error getting current LSN: %v", err)
		return 0, fmt.Errorf("failed to get current LSN for partition %s: %v", partitionKey, err)
	}

	// Calculate the lag
	lag := latestLSN - currentLSN
	log.Printf("Estimated lag for partition key %s: %d", partitionKey, lag)
	return lag, nil
}

func CreateCosmosClient(endpointOrConnection string, useCredentials bool, clientId string, applicationName string) *azcosmos.Client {
	var client *azcosmos.Client
	var err error
	var credential azcore.TokenCredential

	if useCredentials {
		// Create a chained credential
		credential, err = GetChainedCredential(clientId)
		if err != nil {
			log.Fatalf("failed to get credentials: %v", err)
		}
		client, err = azcosmos.NewClient(endpointOrConnection, credential, &azcosmos.ClientOptions{
			// ConnectionMode field removed as it is not valid for azcosmos.ClientOptions
		})
	} else {
		client, err = azcosmos.NewClient(endpointOrConnection, nil, &azcosmos.ClientOptions{})
	}

	if err != nil {
		log.Fatalf("failed to create Cosmos client: %v", err)
	}

	return client
}

func GetChainedCredential(clientId string) (azcore.TokenCredential, error) {
	// Create the DefaultAzureCredential with the ManagedIdentityClientId option
	cred1, err := azidentity.NewDefaultAzureCredential(&azidentity.DefaultAzureCredentialOptions{})
	if err != nil {
		return nil, err
	}
	cred2, err := azidentity.NewWorkloadIdentityCredential(&azidentity.WorkloadIdentityCredentialOptions{
		ClientID: clientId,
	})
	if err != nil {
		return nil, err
	}

	credential, err := azidentity.NewChainedTokenCredential(
		[]azcore.TokenCredential{cred1, cred2}, nil)
	if err != nil {
		return nil, err
	}

	return credential, nil
}

func main() {
	log.Println("Starting the Cosmos DB Change Feed Processor...")
	endpoint := "" // Your Cosmos DB endpoint

	// The client ID of the User Assigned Managed Identity
	uamiClientID := "" // Your UAMI client ID

	// Initialize the Cosmos Client
	client := CreateCosmosClient(endpoint, true, uamiClientID, "test-go")

	// Create container clients
	container, err := client.NewContainer("StoreDatabase", "OrderContainer")
	if err != nil {
		log.Fatalf("Failed to create container client: %v", err)
	}

	leaseContainer, err := client.NewContainer("StoreDatabase", "OrderProcessorLeases")
	if err != nil {
		log.Fatalf("Failed to create lease container client: %v", err)
	}

	// Example: Calculate lag for a specific partition
	partitionKey := "/id" // Replace with the actual partition key

	for {
		lag, err := calculateLag(container, leaseContainer, partitionKey)
		if err != nil {
			log.Printf("Error calculating lag: %v", err)
		} else {
			log.Printf("Estimated lag for partition key %s: %d", partitionKey, lag)
		}
		log.Printf("Sleeping for 5 seconds...")
		time.Sleep(5 * time.Second)
		log.Printf("Done sleeping!")
	}
}
