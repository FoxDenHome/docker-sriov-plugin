package driver

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/docker/go-plugins-helpers/network"
	"github.com/docker/libnetwork/netlabel"
	"github.com/docker/libnetwork/options"
	"github.com/k8snetworkplumbingwg/sriovnet"
)

const (
	containerVethPrefix = "eth"
	networkDevice       = "netdevice" // netdevice interface -o netdevice

	networkMode       = "mode"
	networkModePT     = "passthrough"
	networkModeSRIOV  = "sriov"
	sriovVlan         = "vlan"
	networkPrivileged = "privileged"
	ethPrefix         = "prefix"
	roceHopLimit      = "rocehoplimit"
)

type ptEndpoint struct {
	/* key */
	id string

	/* value */
	HardwareAddr string
	devName      string
	Address      string
	sandboxKey   string
	vfObj        *sriovnet.VfObj
}

type genericNetwork struct {
	id            string
	IPv4Data      *network.IPAMData
	ndevEndpoints map[string]*ptEndpoint
	mode          string // SRIOV or Passthough
	ethPrefix     string

	ndevName string
}

type ptNetwork struct {
	genNw *genericNetwork
}

type NwIface interface {
	CreateNetwork(d *driver, genNw *genericNetwork,
		nid string, options map[string]string,
		ipv4Data *network.IPAMData) error
	DeleteNetwork(d *driver, req *network.DeleteNetworkRequest)

	CreateEndpoint(r *network.CreateEndpointRequest) (*network.CreateEndpointResponse, error)
	DeleteEndpoint(endpoint *ptEndpoint)

	getGenNw() *genericNetwork
}

type driver struct {
	// below map maps a network id to NwInterface object
	networks map[string]NwIface
	sync.Mutex
}

func createGenNw(nid string, ndevName string,
	networkMode string, ethPrefix string, ipv4Data *network.IPAMData) *genericNetwork {

	genNw := genericNetwork{}
	ndevs := map[string]*ptEndpoint{}
	genNw.id = nid
	genNw.mode = networkMode
	genNw.IPv4Data = ipv4Data
	genNw.ndevEndpoints = ndevs
	genNw.ndevName = ndevName
	genNw.ethPrefix = ethPrefix

	return &genNw
}

func (d *driver) GetCapabilities() (*network.CapabilitiesResponse, error) {
	return &network.CapabilitiesResponse{Scope: network.LocalScope}, nil
}

// parseNetworkGenericOptions parses generic driver docker network options
func parseNetworkGenericOptions(data interface{}) (map[string]string, error) {
	var err error

	options := make(map[string]string)

	switch opt := data.(type) {
	case map[string]interface{}:
		for key, value := range opt {
			options[key] = fmt.Sprintf("%s", value)
		}
		log.Printf("parseNetworkGenericOptions %v\n", options)
	default:
		log.Printf("unrecognized network config format: %v\n", reflect.TypeOf(opt))
	}

	if options[networkMode] == "" {
		// default to sriov
		options[networkMode] = networkModeSRIOV
	} else {
		if options[networkMode] != networkModePT &&
			options[networkMode] != networkModeSRIOV {
			return options, fmt.Errorf("valid modes are: passthrough and sriov")
		}
	}
	if options[networkDevice] == "" {
		if options[networkMode] == networkModeSRIOV {
			return options, fmt.Errorf("sriov mode requires netdevice")
		} else {
			return options, fmt.Errorf("passthrough mode requires netdevice")
		}
	}

	if options[ethPrefix] == "" {
		options[ethPrefix] = containerVethPrefix
	}

	return options, err
}

func parseNetworkOptions(id string, option options.Generic) (map[string]string, error) {
	// parse generic labels first
	genData, ok := option[netlabel.GenericData]
	if ok && genData != nil {
		options, err := parseNetworkGenericOptions(genData)

		return options, err
	}
	return nil, fmt.Errorf("invalid options")
}

func (d *driver) createNetwork(nid string, options map[string]string,
	ipv4Data *network.IPAMData, storeConfig bool) error {
	var err error

	genNw := createGenNw(nid, options[networkDevice], options[networkMode], options[ethPrefix], ipv4Data)

	var nw NwIface
	if options[networkMode] == "passthrough" {
		nw = &ptNetwork{}
	} else {
		log.Println("Single port driver for device: ", options[networkDevice])
		nw = &sriovNetwork{}
	}

	err = nw.CreateNetwork(d, genNw, nid, options, ipv4Data)
	if err != nil {
		return err
	}
	d.networks[nid] = nw

	if storeConfig {
		nwDbEntry := DbNetworkInfo{}
		nwDbEntry.Mode = options[networkMode]
		nwDbEntry.Netdev = options[networkDevice]
		nwDbEntry.Vlan, _ = strconv.Atoi(options[sriovVlan])
		nwDbEntry.Gateway = ipv4Data.Gateway
		nwDbEntry.Prefix = options[ethPrefix]

		if options[networkPrivileged] == "1" {
			nwDbEntry.Privileged = true
		} else {
			nwDbEntry.Privileged = false
		}

		err = WriteNwConfigToDB(nid, &nwDbEntry)
		if err != nil {
			return err
		}
	}

	return nil
}

func (d *driver) CreateNetwork(req *network.CreateNetworkRequest) error {
	var err error

	log.Printf("CreateNetwork() : [ %+v ]\n", req)
	log.Printf("CreateNetwork IPv4Data len : [ %v ]\n", len(req.IPv4Data))

	d.Lock()
	defer d.Unlock()

	if len(req.IPv4Data) == 0 {
		return errors.New("network gateway config miss")
	}

	options, ret := parseNetworkOptions(req.NetworkID, req.Options)
	if ret != nil {
		log.Printf("CreateNetwork network options parse error")
		return ret
	}

	ipv4Data := req.IPv4Data[0]

	err = d.createNetwork(req.NetworkID, options, ipv4Data, true)
	return err
}

func (d *driver) AllocateNetwork(r *network.AllocateNetworkRequest) (*network.AllocateNetworkResponse, error) {
	log.Printf("AllocateNetwork() [ %+v ]\n", r)
	return nil, nil
}

func (d *driver) DeleteNetwork(req *network.DeleteNetworkRequest) error {
	log.Printf("DeleteNetwork() [ %+v ]\n", req)

	d.Lock()
	defer d.Unlock()

	nw := d.networks[req.NetworkID]
	if nw != nil {
		nw.DeleteNetwork(d, req)
	}

	delete(d.networks, req.NetworkID)

	DeleteNwConfigFromDB(req.NetworkID)
	return nil
}

func (d *driver) FreeNetwork(r *network.FreeNetworkRequest) error {
	log.Printf("FreeNetwork() [ %+v ]\n", r)
	return nil
}

func BuildNetworkOptions(nwDbEntry *DbNetworkInfo) (map[string]string, error) {
	options := make(map[string]string)

	options[networkDevice] = nwDbEntry.Netdev
	options[networkMode] = nwDbEntry.Mode
	options[sriovVlan] = strconv.Itoa(nwDbEntry.Vlan)
	if nwDbEntry.Privileged {
		options[networkPrivileged] = "1"
	} else {
		options[networkPrivileged] = "0"
	}
	options[ethPrefix] = nwDbEntry.Prefix
	return options, nil
}

func (d *driver) CreatePersistentNetworks() error {
	nwList, err := ReadAllNwConfigs(persistConfigPath)
	if err != nil {
		return err
	}

	for id, info := range nwList {
		options, _ := BuildNetworkOptions(info)

		ipv4Data := network.IPAMData{}
		ipv4Data.Gateway = info.Gateway

		/* Create nw, but ignore the error.
		 * This can happen when plugin is stopped and networks are
		 * Deleted at the docker engine level, which plugin is
		 * completely unaware of.
		 */
		_ = d.createNetwork(id, options, &ipv4Data, false)
	}
	return nil
}

func (d *driver) ValidatePersistentNetworks() error {
	nwList, err := ReadAllNwConfigs(persistConfigPath)
	if err != nil {
		return err
	}

	validNetworks, err := GetNetworkList()
	if err != nil {
		return err
	}

	d.Lock()
	defer d.Unlock()

	for id, _ := range nwList {
		_, valid := validNetworks[id]
		if !valid {
			nwDir := filepath.Join(persistConfigPath, id)
			os.RemoveAll(nwDir)
			log.Println("Deleting stale network: ", id)

			delete(d.networks, id)
		}
	}
	return nil
}

func (d *driver) loopValidatePersistentNetworks() {
	for {
		err := d.ValidatePersistentNetworks()
		if err == nil {
			break
		}
		time.Sleep(time.Second * 5)
	}
	log.Printf("loopValidatePersistentNetworks() done")
}

func StartDriver() (*driver, error) {
	driver := &driver{
		networks: make(map[string]NwIface),
	}

	err := driver.CreatePersistentNetworks()
	if err != nil {
		return nil, err
	}

	go driver.loopValidatePersistentNetworks()

	return driver, nil
}

func (d *driver) CreateEndpoint(r *network.CreateEndpointRequest) (*network.CreateEndpointResponse, error) {
	d.Lock()
	defer d.Unlock()

	log.Printf("CreateEndpoint() [ %+v ]\n", r)
	log.Printf("r.Interface: [ %+v ]\n", r.Interface)

	nw := d.networks[r.NetworkID]
	if nw == nil {
		return nil, fmt.Errorf("plugin can not find network [ %s ]", r.NetworkID)
	}

	return nw.CreateEndpoint(r)
}

func getEndpoint(genNw *genericNetwork, endpointID string) *ptEndpoint {
	return genNw.ndevEndpoints[endpointID]
}

func (nw *ptNetwork) getGenNw() *genericNetwork {
	return nw.genNw
}

func (d *driver) getGenNwFromNetworkID(networkID string) *genericNetwork {
	nw := d.networks[networkID]
	if nw == nil {
		return nil
	}
	return nw.getGenNw()
}

func (d *driver) EndpointInfo(r *network.InfoRequest) (*network.InfoResponse, error) {
	log.Printf("EndpointInfo: [ %+v ]\n", r)
	d.Lock()
	defer d.Unlock()

	genNw := d.getGenNwFromNetworkID(r.NetworkID)
	if genNw == nil {
		return nil, fmt.Errorf("can not find network [ %s ]", r.NetworkID)
	}

	endpoint := getEndpoint(genNw, r.EndpointID)
	if endpoint == nil {
		return nil, fmt.Errorf("cannot find endpoint by id: %s", r.EndpointID)
	}

	value := make(map[string]string)
	value["id"] = endpoint.id
	value["srcName"] = endpoint.devName
	resp := &network.InfoResponse{
		Value: value,
	}
	log.Printf("EndpointInfo resp.Value : [ %+v ]\n", resp.Value)
	return resp, nil
}

func (d *driver) Join(r *network.JoinRequest) (*network.JoinResponse, error) {
	log.Printf("Join() [ %+v ]\n", r)

	d.Lock()
	defer d.Unlock()

	genNw := d.getGenNwFromNetworkID(r.NetworkID)
	if genNw == nil {
		return nil, fmt.Errorf("can not find network [ %s ]", r.NetworkID)
	}

	endpoint := getEndpoint(genNw, r.EndpointID)
	if endpoint == nil {
		return nil, fmt.Errorf("cannot find endpoint by id: %s", r.EndpointID)
	}

	if endpoint.sandboxKey != "" {
		return nil, fmt.Errorf("endpoint [%s] has bean bind to sandbox [%s]", r.EndpointID, endpoint.sandboxKey)
	}
	gw, _, err := net.ParseCIDR(genNw.IPv4Data.Gateway)
	if err != nil {
		return nil, fmt.Errorf("parse gateway [%s] error: %s", genNw.IPv4Data.Gateway, err.Error())
	}
	endpoint.sandboxKey = r.SandboxKey
	resp := network.JoinResponse{
		InterfaceName: network.InterfaceName{
			SrcName:   endpoint.devName,
			DstPrefix: genNw.ethPrefix,
		},
		DisableGatewayService: false,
		Gateway:               gw.String(),
	}

	log.Printf("Join resp : [ %+v ]\n", resp)
	return &resp, nil
}

func (d *driver) Leave(r *network.LeaveRequest) error {
	log.Printf("Leave(): [ %+v ]\n", r)
	d.Lock()
	defer d.Unlock()

	genNw := d.getGenNwFromNetworkID(r.NetworkID)
	if genNw == nil {
		return fmt.Errorf("can not find network [ %s ]", r.NetworkID)
	}

	endpoint := getEndpoint(genNw, r.EndpointID)
	if endpoint == nil {
		return fmt.Errorf("cannot find endpoint by id: %s", r.EndpointID)
	}

	endpoint.sandboxKey = ""
	return nil
}

func (d *driver) DeleteEndpoint(r *network.DeleteEndpointRequest) error {
	log.Printf("DeleteEndpoint() [ %+v ]\n", r)

	d.Lock()
	defer d.Unlock()

	genNw := d.getGenNwFromNetworkID(r.NetworkID)
	if genNw == nil {
		return fmt.Errorf("can not find network [ %s ]", r.NetworkID)
	}

	endpoint := getEndpoint(genNw, r.EndpointID)
	if endpoint == nil {
		return fmt.Errorf("cannot find endpoint by id: %s", r.EndpointID)
	}

	nw := d.networks[r.NetworkID]

	nw.DeleteEndpoint(endpoint)
	delete(genNw.ndevEndpoints, r.EndpointID)
	return nil
}

func (d *driver) DiscoverNew(r *network.DiscoveryNotification) error {
	log.Printf("DiscoverNew(): [ %+v ]\n", r)
	return nil
}

func (d *driver) DiscoverDelete(r *network.DiscoveryNotification) error {
	log.Printf("DiscoverDelete: [ %+v ]\n", r)
	return nil
}

func (d *driver) ProgramExternalConnectivity(r *network.ProgramExternalConnectivityRequest) error {
	log.Printf("ProgramExternalConnectivity(): [ %+v ]\n", r)
	return nil
}

func (d *driver) RevokeExternalConnectivity(r *network.RevokeExternalConnectivityRequest) error {
	log.Printf("RevokeExternalConnectivity(): [ %+v ]\n", r)
	return nil
}

func (pt *ptNetwork) CreateNetwork(d *driver, genNw *genericNetwork,
	nid string, options map[string]string,
	ipv4Data *network.IPAMData) error {

	pt.genNw = genNw

	log.Printf("PT CreateNetwork : [%s] IPv4Data : [ %+v ]\n", pt.genNw.id, pt.genNw.IPv4Data)
	return nil
}

func (pt *ptNetwork) DeleteNetwork(d *driver, req *network.DeleteNetworkRequest) {

}

func (nw *ptNetwork) CreateEndpoint(r *network.CreateEndpointRequest) (*network.CreateEndpointResponse, error) {
	if len(nw.genNw.ndevEndpoints) > 0 {
		return nil, fmt.Errorf("supports only one device")
	}

	ndev := &ptEndpoint{
		devName: nw.genNw.ndevName,
		Address: r.Interface.Address,
	}
	nw.genNw.ndevEndpoints[r.EndpointID] = ndev

	endpointInterface := &network.EndpointInterface{}
	if r.Interface.Address == "" {
		endpointInterface.Address = ndev.Address
	}
	resp := &network.CreateEndpointResponse{Interface: endpointInterface}
	log.Printf("PT CreateEndpoint resp interface: [ %+v ] ", resp.Interface)
	return resp, nil
}

func (nw *ptNetwork) DeleteEndpoint(endpoint *ptEndpoint) {

}
