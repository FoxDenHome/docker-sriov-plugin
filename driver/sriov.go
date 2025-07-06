package driver

import (
	"errors"
	"fmt"
	"log"
	"strconv"

	"github.com/docker/go-plugins-helpers/network"
	"github.com/k8snetworkplumbingwg/sriovnet"
)

const (
	SRIOV_ENABLED    = "enabled"
	SRIOV_DISABLED   = "disabled"
	sriovUnsupported = "unsupported"
)

type pfDevice struct {
	pfHandle      *sriovnet.PfNetdevHandle
	state         string
	nwUseRefCount int
}

type sriovNetwork struct {
	genNw        *genericNetwork
	vlan         int
	privileged   int
	roceHopLimit uint8
}

// nid to network map
// key = nid
// value = sriovNetwork
var networks map[string]*sriovNetwork

// netdevice to sriovstate map
// key = phy netdevice
// value = its sriov state/information
var pfDevices map[string]*pfDevice

func checkVlanNwExist(pfNetdevName string, vlan int) bool {
	if vlan == 0 {
		return false
	}

	for _, nw := range networks {
		if nw.vlan == vlan && nw.genNw.ndevName == pfNetdevName {
			return true
		}
	}
	return false
}

func (nw *sriovNetwork) getGenNw() *genericNetwork {
	return nw.genNw
}

func (nw *sriovNetwork) CreateNetwork(d *driver, genNw *genericNetwork,
	nid string, options map[string]string,
	ipv4Data *network.IPAMData) error {
	var err error
	var vlan int
	var privileged int

	ndevName := options[networkDevice]

	if options[sriovVlan] != "" {
		vlan, _ = strconv.Atoi(options[sriovVlan])
		if vlan < 0 || vlan > 4095 {
			return errors.New("invalid vlan id given")
		}
		if checkVlanNwExist(ndevName, vlan) {
			return errors.New("vlan already exist")
		}
	}
	if options[networkPrivileged] != "" {
		privileged, _ = strconv.Atoi(options[networkPrivileged])
	}
	nw.privileged = privileged

	if options[roceHopLimit] != "" {
		var err1 error
		var value int
		value, err1 = strconv.Atoi(options[roceHopLimit])
		if err1 != nil {
			return fmt.Errorf("invalid roceHopLimit: %w", err1)
		}
		if value < 0 || value > 255 {
			return errors.New("valid range of rocehoplimit is: [0..255]")
		}
		nw.roceHopLimit = uint8(value)
	}

	nw.genNw = genNw

	err = nw.DiscoverVFs(ndevName)
	if err != nil {
		return err
	}
	// store vlan so that when VFs are attached to container, vlan will be set at that time
	nw.vlan = vlan
	if len(networks) == 0 {
		networks = make(map[string]*sriovNetwork)
	}

	networks[nid] = nw

	dev := pfDevices[ndevName]
	dev.nwUseRefCount++
	log.Printf("SRIOV CreateNetwork : [%s] IPv4Data : [ %+v ]\n", nw.genNw.id, nw.genNw.IPv4Data)
	return nil
}

func initSriovState(pfNetdevName string, dev *pfDevice) error {
	var err error

	if !sriovnet.IsSriovEnabled(pfNetdevName) {
		return errors.New("sriov not enabled")
	}

	dev.pfHandle, err = sriovnet.GetPfNetdevHandle(pfNetdevName)
	if err != nil {
		log.Println("fail to get handle: ", pfNetdevName, err)
		return fmt.Errorf("fail to get device handle: %w", err)
	}

	dev.state = SRIOV_ENABLED
	return nil
}

func (nw *sriovNetwork) DiscoverVFs(pfNetdevName string) error {
	var err error

	if len(pfDevices) == 0 {
		pfDevices = make(map[string]*pfDevice)
	}

	dev := pfDevices[pfNetdevName]
	if dev == nil {
		newDev := pfDevice{}
		err = initSriovState(pfNetdevName, &newDev)
		if err != nil {
			return err
		}
		pfDevices[pfNetdevName] = &newDev
		dev = &newDev
	}
	return nil
}

func (nw *sriovNetwork) CreateEndpoint(r *network.CreateEndpointRequest) (*network.CreateEndpointResponse, error) {
	var vfObj *sriovnet.VfObj
	var err error
	var privileged bool

	if nw.privileged > 0 {
		privileged = true
	} else {
		privileged = false
	}

	dev := pfDevices[nw.genNw.ndevName]
	if dev.pfHandle == nil {
		return nil, errors.New("invalid SRIOV configuration")
	}

	if r.Interface.MacAddress != "" {
		vfObj, err = sriovnet.AllocateVfByMacAddress(dev.pfHandle, r.Interface.MacAddress)
	} else {
		vfObj, err = sriovnet.AllocateVf(dev.pfHandle)
	}

	if err != nil {
		return nil, fmt.Errorf("fail to allocate VF err = %w", err)
	}

	if nw.vlan > 0 {
		sriovnet.SetVfVlan(dev.pfHandle, vfObj, nw.vlan)
	}

	err2 := sriovnet.SetVfPrivileged(dev.pfHandle, vfObj, privileged)
	if err2 != nil {
		sriovnet.FreeVf(dev.pfHandle, vfObj)
		return nil, fmt.Errorf("fail to set priviledged err = %w", err2)
	}

	if nw.roceHopLimit != 0 {
		err = setRoceHopLimitWA(sriovnet.GetVfNetdevName(dev.pfHandle, vfObj), nw.roceHopLimit)
		if err != nil {
			return nil, fmt.Errorf("fail to set RoCE Hoplimit = %w", err)
		}
	}

	log.Printf("AllocVF PF [ %+v ] vf:%v\n", nw.genNw.ndevName, vfObj)

	ndev := &ptEndpoint{
		devName: sriovnet.GetVfNetdevName(dev.pfHandle, vfObj),
		vfObj:   vfObj,
		Address: r.Interface.Address,
	}
	nw.genNw.ndevEndpoints[r.EndpointID] = ndev

	endpointInterface := &network.EndpointInterface{}
	if r.Interface.Address == "" {
		endpointInterface.Address = ndev.Address
	}
	resp := &network.CreateEndpointResponse{Interface: endpointInterface}

	log.Printf("SRIOV CreateEndpoint resp interface: [ %+v ]\n", resp.Interface)
	return resp, nil
}

func (nw *sriovNetwork) DeleteEndpoint(endpoint *ptEndpoint) {
	dev := pfDevices[nw.genNw.ndevName]
	sriovnet.FreeVf(dev.pfHandle, endpoint.vfObj)
}

func (nw *sriovNetwork) DeleteNetwork(d *driver, req *network.DeleteNetworkRequest) {
	dev := pfDevices[nw.genNw.ndevName]
	dev.nwUseRefCount--

	// multiple vlan based network will share enabled VFs.
	// So first created network enables SRIOV and
	// Last network that gets deleted, disables SRIOV.
	if dev.nwUseRefCount == 0 {
		delete(pfDevices, nw.genNw.ndevName)
	}
	delete(networks, nw.genNw.id)
	log.Printf("DeleteNetwork: total networks = %d\n", len(networks))
}
