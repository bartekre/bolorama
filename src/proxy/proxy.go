package proxy

import (
	"fmt"
	"net"
	"strings"
	"sync"
)

const firstPlayerPort = 40001

// The largest safe UDP packet length is 576 for IPv4 and 1280 for IPv6, where
// "safe" is defined as “guaranteed to be able to be reassembled, if fragmented."
const bufferSize = 1024

// Route associates a proxy port with a player's real IP address + port
type Route struct {
	PlayerIPAddr net.UDPAddr
	ProxyPort    int
	Connection   *net.UDPConn
	RxChannel    chan UdpPacket
	TxChannel    chan UdpPacket
}

// UdpPacket represents a packet being sent from srcAddr to dstAddr
type UdpPacket struct {
	SrcAddr net.UDPAddr
	DstAddr net.UDPAddr
	DstPort int
	Len     int
	Buffer  []byte
}

var assignedPlayerPorts []int

// 0 <= index <= len(a)
func insert(a []int, index int, value int) []int {
	if len(a) == index { // nil or empty slice or after last element
		return append(a, value)
	}
	a = append(a[:index+1], a[index:]...) // index < len(a)
	a[index] = value
	return a
}

func getNextAvailablePort(firstPort int, assignedPorts *[]int) int {
	nextPort := firstPort
	portCount := len(*assignedPorts)

	if portCount == 0 {
		*assignedPorts = append(*assignedPorts, nextPort)
		return nextPort
	}

	// use a first hole in port list, if one exists
	for i, port := range *assignedPorts {
		if port == nextPort {
			nextPort = port + 1
		} else {
			*assignedPorts = insert(*assignedPorts, i, nextPort)
			break
		}
	}

	lastPort := (*assignedPorts)[len(*assignedPorts)-1]
	if nextPort > lastPort {
		*assignedPorts = append(*assignedPorts, nextPort)
	}

	return nextPort
}

func GetRouteByAddr(gameIDRouteTableMap map[[8]byte][]Route, addr net.UDPAddr) (Route, error) {
	for _, routes := range gameIDRouteTableMap {
		for _, route := range routes {
			if addr.IP.Equal(route.PlayerIPAddr.IP) && addr.Port == route.PlayerIPAddr.Port {
				return route, nil
			}
		}
	}

	return Route{}, fmt.Errorf("Error: Socket %s:%d not found in routing tables",
		addr.IP.String(), addr.Port)
}

func GetRouteByPort(gameIDRouteTableMap map[[8]byte][]Route, port int) ([8]byte, Route, error) {
	for gameID, routes := range gameIDRouteTableMap {
		for _, route := range routes {
			if port == route.ProxyPort {
				return gameID, route, nil
			}
		}
	}

	return [8]byte{}, Route{}, fmt.Errorf("Error: Port %d not found in routing tables", port)
}

func AddPlayer(wg *sync.WaitGroup, controlChannel chan int, srcAddr net.UDPAddr, rxChannel chan UdpPacket) Route {
	nextPlayerPort := getNextAvailablePort(firstPlayerPort, &assignedPlayerPorts)
	playerRoute := newPlayerRoute(srcAddr, nextPlayerPort, rxChannel)
	createPlayerProxy(wg, controlChannel, playerRoute)
	return playerRoute
}

func newPlayerRoute(addr net.UDPAddr, port int, rxChannel chan UdpPacket) Route {
	txChannel := make(chan UdpPacket)

	return Route{
		addr,
		port,
		nil,
		rxChannel,
		txChannel,
	}
}

func createPlayerProxy(wg *sync.WaitGroup, controlChannel chan int, playerRoute Route) {
	fmt.Println()
	fmt.Printf("Creating proxy: %d => %s:%d\n", playerRoute.ProxyPort,
		playerRoute.PlayerIPAddr.IP.String(), playerRoute.PlayerIPAddr.Port)

	listenAddr, err := net.ResolveUDPAddr("udp4", fmt.Sprint(":", playerRoute.ProxyPort))
	if err != nil {
		fmt.Println(err)
		return
	}

	connection, err := net.ListenUDP("udp4", listenAddr)
	if err != nil {
		fmt.Println(err)
		return
	}

	playerRoute.Connection = connection

	wg.Add(2)
	go udpListener(wg, controlChannel, playerRoute)
	go udpTransmitter(wg, controlChannel, playerRoute)
}

func udpListener(wg *sync.WaitGroup, controlChannel chan int, playerRoute Route) {
	buffer := make([]byte, bufferSize)

	defer wg.Done()

	go func() {
		for {
			port, ok := <-controlChannel
			if port == playerRoute.ProxyPort || !ok {
				playerRoute.Connection.Close()
			}
			if !ok {
				break
			}
		}
	}()

	for {
		n, addr, err := playerRoute.Connection.ReadFromUDP(buffer)
		if err != nil {
			if !strings.HasSuffix(err.Error(), "use of closed network connection") {
				fmt.Println(err)
			}
			fmt.Println("Stopped listening on UDP port", playerRoute.ProxyPort)
			break
		}

		data := make([]byte, n)
		copy(data, buffer)
		playerRoute.RxChannel <- UdpPacket{*addr, net.UDPAddr{}, playerRoute.ProxyPort, n, data}
	}
}

func udpTransmitter(wg *sync.WaitGroup, controlChannel chan int, playerRoute Route) {
	defer wg.Done()
	defer func() {
		fmt.Println("Stopped transmitting on UDP port", playerRoute.ProxyPort)
	}()

	for {
		select {
		case port, ok := <-controlChannel:
			if port == playerRoute.ProxyPort || !ok {
				return
			}
		case data := <-playerRoute.TxChannel:
			_, err := playerRoute.Connection.WriteToUDP(data.Buffer, &data.DstAddr)
			if err != nil {
				fmt.Println(err)
			}
		}
	}
}
