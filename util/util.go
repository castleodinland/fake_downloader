package util

import (
	"encoding/hex"
	// "crypto/sha1"
	// "errors"
	"math/rand"
	// "os"
	"fmt"
	"log"
	"net"
	"sync/atomic"
	"time"
)

func RandomPeerId() string {
	chars := "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-_.!~*()"
	ret := "-qB4630-"

	for i := 0; i < 12; i++ {
		ret += string(chars[rand.Intn(len(chars))])
	}

	return ret
}

// Connect to a peer and spam request piece messages with stop functionality

func ConnectPeerWithStop(peerAddr string, infoHash string, stopChan chan struct{}, speed *int64) {
	conn, err := net.Dial("tcp", peerAddr)
	if err != nil {
		panic(err)
	}
	defer conn.Close()

	infoHashBytes, err := hex.DecodeString(infoHash)
	if err != nil {
		panic(err)
	}
	payload := []byte("\x13" + "BitTorrent protocol" + "\x00\x00\x00\x00\x00\x00\x00\x00" + string(infoHashBytes) + RandomPeerId())

	_, err = conn.Write(payload)
	if err != nil {
		panic(err)
	}

	fmt.Println("Handshake sent: ", string(payload))

	buf := make([]byte, 102400)
	n, err := conn.Read(buf)
	if err != nil {
		panic(err)
	}

	// fmt.Println("Received: ", string(buf[:n]))

	payload = []byte("\x00\x00\x00\x01\x02")
	_, err = conn.Write(payload)
	if err != nil {
		panic(err)
	}

	count := 0
	var downloadedBytes int64

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stopChan:
			log.Println("Stopping...")
			return

		case <-ticker.C:
			currentDownloaded := atomic.LoadInt64(&downloadedBytes)
			atomic.StoreInt64(&downloadedBytes, 0)
			currentSpeed := currentDownloaded / 1024  // 转换为 MB/s
			atomic.StoreInt64(speed, currentSpeed)

		default:
			payload = []byte("\x00\x00\x00\x0d" + "\x06" + "\x00\x00\x00\x00" + "\x00\x00\x00\x00" + "\x00\x00\x40\x00")
			n, err = conn.Write(payload)
			// log.Println("Write: ", n, err)

			n, err = conn.Read(buf)
			atomic.AddInt64(&downloadedBytes, int64(n))
			// log.Println("Read: ", n, err)

			count += 1

			if count%100 == 0 {
				// log.Println(count)
			}
		}
	}
}
