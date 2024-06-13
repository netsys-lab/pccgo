package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/netsys-lab/pccgo"
)

func runClient(dest string, interval int) {
	addr, err := net.ResolveUDPAddr("udp", dest)
	if err != nil {
		fmt.Printf("Failed to resolve address: %v\n", err)
		os.Exit(1)
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		fmt.Printf("Failed to dial UDP: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	cc := pccgo.NewCongestionControl(pccgo.CongestionControlOptions{
		PayloadSize: 1024,
	})

	rttSet := false
	timeNow := time.Now()

	go func() {
		// Listen for NACK
		buffer := make([]byte, 1024)
		for {
			_, _, err := conn.ReadFromUDP(buffer)
			if err == nil {
				if !rttSet {
					rtt := time.Since(timeNow).Milliseconds()
					cc.UpdateRTT(uint64(rtt))
					fmt.Println("RTT set to:", rtt, "ms")
					rttSet = true
					continue
				}
				// response := string(buffer[:n])
				// loss, _ := strconv.Atoi(response)
				// fmt.Printf("Received NACK for missing sequence number: %d\n", loss)
				cc.AddLoss(1)
				// fmt.Printf("Received response: %s\n", response)
			}
		}

	}()

	seqNum := 0
	for {

		/*if seqNum%11 == 0 {
			// fmt.Println("Dropping packet")
			seqNum++
			continue
		}*/

		cc.Limit()
		payload := []byte(strconv.Itoa(seqNum))
		_, err := conn.Write(payload)
		if err != nil {
			fmt.Printf("Failed to send packet: %v\n", err)
			os.Exit(1)
		}
		// fmt.Printf("Sent packet with sequence number: %d\n", seqNum)
		// time.Sleep(50 * time.Millisecond)
		seqNum++

		// time.Sleep(time.Duration(interval) * time.Second)

	}
}

func runServer(listenAddr string) {
	addr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		fmt.Printf("Failed to resolve address: %v\n", err)
		os.Exit(1)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		fmt.Printf("Failed to listen on UDP: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	expectedSeqNum := 1
	receivedPackets := make(map[int]bool)

	nackChan := make(chan int)
	var remoteAddr *net.UDPAddr
	go func() {
		for {
			nack := <-nackChan
			payload := []byte(strconv.Itoa(nack))
			_, err := conn.WriteToUDP(payload, remoteAddr)
			if err != nil {
				fmt.Printf("Failed to send NACK: %v\n", err)
			} else {
				fmt.Printf("Sent NACK for missing sequence number: %d\n", nack)
			}

		}
	}()

	for {
		buffer := make([]byte, 1024)
		n, addr, err := conn.ReadFromUDP(buffer)
		remoteAddr = addr
		if err != nil {
			fmt.Printf("Failed to read packet: %v\n", err)
			continue
		}

		seqNum, err := strconv.Atoi(strings.TrimSpace(string(buffer[:n])))
		if err != nil {
			fmt.Printf("Invalid sequence number: %v\n", err)
			continue
		}

		if seqNum == 0 {
			payload := []byte(strconv.Itoa(seqNum))
			conn.WriteToUDP(payload, remoteAddr)
		}

		// fmt.Printf("Received packet with sequence number: %d\n", seqNum)
		receivedPackets[seqNum] = true

		if seqNum != expectedSeqNum {
			fmt.Printf("Received out-of-order packet. Expected: %d, Received: %d\n", expectedSeqNum, seqNum)
			// Get all missing numbers between expectedSeqNum and seqNum
			for i := expectedSeqNum; i < seqNum; i++ {
				nackChan <- i
			}
			expectedSeqNum = seqNum + 1
		} else {
			expectedSeqNum++
		}

	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("Usage: %s <server|client> [<args>]\n", os.Args[0])
		os.Exit(1)
	}

	mode := os.Args[1]
	switch mode {
	case "client":
		if len(os.Args) != 4 {
			fmt.Printf("Usage: %s client <destination IP:port> <interval in seconds>\n", os.Args[0])
			os.Exit(1)
		}
		dest := os.Args[2]
		interval, err := strconv.Atoi(os.Args[3])
		if err != nil {
			fmt.Printf("Invalid interval: %v\n", err)
			os.Exit(1)
		}
		runClient(dest, interval)
	case "server":
		if len(os.Args) != 3 {
			fmt.Printf("Usage: %s server <listening IP:port>\n", os.Args[0])
			os.Exit(1)
		}
		listenAddr := os.Args[2]
		runServer(listenAddr)
	default:
		fmt.Printf("Invalid mode: %s. Use 'server' or 'client'.\n", mode)
		os.Exit(1)
	}
}
