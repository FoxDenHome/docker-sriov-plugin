package driver

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

func getRightClientApiVersion() (string, error) {
	// Start with the lowest API to query which version is supported.
	lowestCli, err3 := client.NewClientWithOpts(client.FromEnv, client.WithVersion("1.24"))
	if err3 != nil {
		fmt.Println("Fail to create client: ", err3)
		return "", err3
	}
	allVersions, err2 := lowestCli.ServerVersion(context.Background())
	if err2 != nil {
		fmt.Println("Error to get server version: ", err2)
		return "", err2
	}
	return allVersions.APIVersion, nil
}

func getRightClient() (*client.Client, error) {
	var clientVersion string

	desiredVersion, err := getRightClientApiVersion()
	if err != nil {
		clientVersion = "unknown"
	} else {
		clientVersion = desiredVersion
	}
	cli, err2 := client.NewClientWithOpts(client.FromEnv, client.WithVersion(clientVersion))
	if err2 == nil {
		return cli, nil
	}
	return nil, err
}

func GetNetworkList() (map[string]network.Summary, error) {
	cli, err := getRightClient()
	if err != nil {
		return nil, err
	}
	networks, err := cli.NetworkList(context.Background(), network.ListOptions{})
	if err != nil {
		return nil, err
	}

	res := make(map[string]network.Summary)
	for _, network := range networks {
		res[network.ID] = network
	}
	return res, nil
}
