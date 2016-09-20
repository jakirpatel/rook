package inventory

import (
	"errors"
	"fmt"
	"log"
	"path"
	"strconv"
	"strings"

	ctx "golang.org/x/net/context"

	"github.com/quantum/castle/pkg/proc"
	"github.com/quantum/castle/pkg/util"

	etcd "github.com/coreos/etcd/client"
)

const (
	IpAddressKey  = "ipaddress"
	DisksKey      = "disks"
	ProcessorsKey = "cpu"
	NetworkKey    = "net"
	MemoryKey     = "mem"
)

func DiscoverHardware(nodeID string, etcdClient etcd.KeysAPI, executor proc.Executor) error {
	nodeConfigKey := GetNodeConfigKey(nodeID)
	if err := discoverDisks(nodeConfigKey, etcdClient, executor); err != nil {
		return err
	}

	// TODO: discover more hardware properties

	return nil
}

// gets the key under which all node hardware/config will be stored
func GetNodeConfigKey(nodeID string) string {
	return path.Join(DiscoveredNodesKey, nodeID)
}

// Load all the nodes' infrastructure configuration
func loadNodeConfig(etcdClient etcd.KeysAPI) (map[string]*NodeConfig, error) {

	// Load the discovered nodes
	nodes, err := util.GetDirChildKeys(etcdClient, DiscoveredNodesKey)
	log.Printf("Discovered %d nodes", nodes.Count())
	if err != nil {
		log.Printf("failed to get the node ids. err=%s", err.Error())
		return nil, err
	}

	nodesConfig := make(map[string]*NodeConfig)
	for node := range nodes.Iter() {
		nodeConfig := &NodeConfig{}

		// get all the config information for the current node
		configKey := GetNodeConfigKey(node)
		nodeInfo, err := etcdClient.Get(ctx.Background(), configKey, &etcd.GetOptions{Recursive: true})
		if err != nil {
			if util.IsEtcdKeyNotFound(err) {
				log.Printf("skipping node %s with no hardware discovered", node)
				continue
			}
			log.Printf("failed to get hardware info from etcd for node %s, %v", node, err)
		} else {
			err = loadHardwareConfig(node, nodeConfig, nodeInfo)
			if err != nil {
				log.Printf("failed to parse hardware config for node %s, %v", node, err)
				return nil, err
			}
		}

		ipAddr, err := GetIpAddress(etcdClient, node)
		if err != nil {
			log.Printf("failed to get IP address for node %s, %+v", node, err)
			return nil, err
		}
		nodeConfig.IPAddress = ipAddr

		nodesConfig[node] = nodeConfig
	}

	return nodesConfig, nil
}

// Get the IP address for a node
func GetIpAddress(etcdClient etcd.KeysAPI, nodeId string) (string, error) {
	key := path.Join(GetNodeConfigKey(nodeId), IpAddressKey)
	val, err := etcdClient.Get(ctx.Background(), key, nil)
	if err != nil {
		log.Printf("FAILED TO GET nodeID for %s. %v", nodeId, err)
		return "", err
	}

	return val.Node.Value, nil
}

// Set the IP address for a node
func SetIpAddress(etcdClient etcd.KeysAPI, nodeId, ipaddress string) error {
	key := path.Join(GetNodeConfigKey(nodeId), IpAddressKey)
	_, err := etcdClient.Set(ctx.Background(), key, ipaddress, nil)

	return err
}

func loadHardwareConfig(nodeId string, nodeConfig *NodeConfig, nodeInfo *etcd.Response) error {
	if nodeInfo == nil || nodeInfo.Node == nil {
		return errors.New("hardware info missing")
	}

	for _, nodeConfigRoot := range nodeInfo.Node.Nodes {
		switch util.GetLeafKeyPath(nodeConfigRoot.Key) {
		case DisksKey:
			err := loadDisksConfig(nodeConfig, nodeConfigRoot)
			if err != nil {
				log.Printf("failed to load disk config for node %s, %v", nodeId, err)
				return err
			}
		case ProcessorsKey:
			err := loadProcessorsConfig(nodeConfig, nodeConfigRoot)
			if err != nil {
				log.Printf("failed to load processor config for node %s, %v", nodeId, err)
				return err
			}
		case MemoryKey:
			err := loadMemoryConfig(nodeConfig, nodeConfigRoot)
			if err != nil {
				log.Printf("failed to load memory config for node %s, %v", nodeId, err)
				return err
			}
		case NetworkKey:
			err := loadNetworkConfig(nodeConfig, nodeConfigRoot)
			if err != nil {
				log.Printf("failed to load network config for node %s, %v", nodeId, err)
				return err
			}
		case IpAddressKey:
			err := loadIPAddressConfig(nodeConfig, nodeConfigRoot)
			if err != nil {
				log.Printf("failed to load IP address config for node %s, %v", nodeId, err)
				return err
			}
		default:
			log.Printf("unexpected hardware component: %s, skipping...", nodeConfigRoot)
		}
	}

	return nil
}

func loadDisksConfig(nodeConfig *NodeConfig, disksRootNode *etcd.Node) error {
	numDisks := 0
	if disksRootNode.Nodes != nil {
		numDisks = len(disksRootNode.Nodes)
	}

	nodeConfig.Disks = make([]DiskConfig, numDisks)

	// iterate over all disks from etcd
	for i, diskInfo := range disksRootNode.Nodes {
		disk, err := GetDiskInfo(diskInfo)
		if err != nil {
			log.Printf("Failed to get disk. err=%v", err)
			return err
		}

		nodeConfig.Disks[i] = *disk

	}

	return nil
}

func loadProcessorsConfig(nodeConfig *NodeConfig, procsRootNode *etcd.Node) error {
	numProcs := 0
	if procsRootNode.Nodes != nil {
		numProcs = len(procsRootNode.Nodes)
	}

	nodeConfig.Processors = make([]ProcessorConfig, numProcs)

	// iterate over all processors from etcd
	for i, procInfo := range procsRootNode.Nodes {
		proc := ProcessorConfig{}
		if procID, err := strconv.ParseUint(util.GetLeafKeyPath(procInfo.Key), 10, 32); err != nil {
			return err
		} else {
			proc.ID = uint(procID)
		}

		// iterate over all properties of the processor
		for _, procProperty := range procInfo.Nodes {
			procPropertyName := util.GetLeafKeyPath(procProperty.Key)
			switch procPropertyName {
			case ProcPhysicalIDKey:
				if phsyicalId, err := strconv.ParseUint(procProperty.Value, 10, 32); err != nil {
					return err
				} else {
					proc.PhysicalID = uint(phsyicalId)
				}
			case ProcSiblingsKey:
				if siblings, err := strconv.ParseUint(procProperty.Value, 10, 32); err != nil {
					return err
				} else {
					proc.Siblings = uint(siblings)
				}
			case ProcCoreIDKey:
				if coreId, err := strconv.ParseUint(procProperty.Value, 10, 32); err != nil {
					return err
				} else {
					proc.CoreID = uint(coreId)
				}
			case ProcNumCoresKey:
				if numCores, err := strconv.ParseUint(procProperty.Value, 10, 32); err != nil {
					return err
				} else {
					proc.NumCores = uint(numCores)
				}
			case ProcSpeedKey:
				if speed, err := strconv.ParseFloat(procProperty.Value, 64); err != nil {
					return err
				} else {
					proc.Speed = speed
				}
			case ProcBitsKey:
				if numBits, err := strconv.ParseUint(procProperty.Value, 10, 32); err != nil {
					return err
				} else {
					proc.Bits = uint(numBits)
				}
			default:
				log.Printf("unknown processor property key %s, skipping", procPropertyName)
			}
		}

		nodeConfig.Processors[i] = proc
	}

	return nil
}

func loadMemoryConfig(nodeConfig *NodeConfig, memoryRootNode *etcd.Node) error {
	mem := MemoryConfig{}
	for _, memProperty := range memoryRootNode.Nodes {
		memPropertyName := util.GetLeafKeyPath(memProperty.Key)
		switch memPropertyName {
		case MemoryTotalSizeKey:
			if size, err := strconv.ParseUint(memProperty.Value, 10, 64); err != nil {
				return err
			} else {
				mem.TotalSize = size
			}
		default:
			log.Printf("unknown memory property key %s, skipping", memPropertyName)
		}
	}

	nodeConfig.Memory = mem
	return nil
}

func loadNetworkConfig(nodeConfig *NodeConfig, networkRootNode *etcd.Node) error {
	numNics := 0
	if networkRootNode.Nodes != nil {
		numNics = len(networkRootNode.Nodes)
	}

	nodeConfig.NetworkAdapters = make([]NetworkConfig, numNics)

	// iterate over all network adapters from etcd
	for i, netInfo := range networkRootNode.Nodes {
		net := NetworkConfig{}
		net.Name = util.GetLeafKeyPath(netInfo.Key)

		// iterate over all properties of the network adapter
		for _, netProperty := range netInfo.Nodes {
			netPropertyName := util.GetLeafKeyPath(netProperty.Key)
			switch netPropertyName {
			case NetworkIPv4AddressKey:
				net.IPv4Address = netProperty.Value
			case NetworkIPv6AddressKey:
				net.IPv6Address = netProperty.Value
			case NetworkSpeedKey:
				if netProperty.Value == "" {
					net.Speed = 0
				} else if speed, err := strconv.ParseUint(netProperty.Value, 10, 64); err != nil {
					return err
				} else {
					net.Speed = speed
				}
			default:
				log.Printf("unknown network adapter property key %s, skipping", netPropertyName)
			}
		}

		nodeConfig.NetworkAdapters[i] = net
	}

	return nil
}

func loadIPAddressConfig(nodeConfig *NodeConfig, ipAddressNode *etcd.Node) error {
	if ipAddressNode.Dir {
		return fmt.Errorf("IP address node '%s' is a directory, but it's expected to be a key", ipAddressNode.Key)
	}
	nodeConfig.IPAddress = ipAddressNode.Value
	return nil
}

// converts a raw key value pair string into a map of key value pairs
// example raw string of `foo="0" bar="1" baz="biz"` is returned as:
// map[string]string{"foo":"0", "bar":"1", "baz":"biz"}
func parseKeyValuePairString(propsRaw string) map[string]string {
	// first split the single raw string on spaces and initialize a map of
	// a length equal to the number of pairs
	props := strings.Split(propsRaw, " ")
	propMap := make(map[string]string, len(props))

	for _, kvpRaw := range props {
		// split each individual key value pair on the equals sign
		kvp := strings.Split(kvpRaw, "=")
		if len(kvp) == 2 {
			// first element is the final key, second element is the final value
			// (don't forget to remove surrounding quotes from the value)
			propMap[kvp[0]] = strings.Replace(kvp[1], `"`, "", -1)
		}
	}

	return propMap
}