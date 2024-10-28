package util

import (
	"encoding/hex"
	"fmt"
	"log"
	"math/rand"
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

func ConnectPeerWithStop(peerAddr string, infoHash string, stopChan chan struct{}, speed *int64) error {
	if peerAddr == "" {
		return fmt.Errorf("peer address is empty")
	}
	if infoHash == "" {
		return fmt.Errorf("info hash is empty")
	}

	conn, err := net.Dial("tcp", peerAddr)
	if err != nil {
		return fmt.Errorf("failed to connect to peer: %w", err)
	}
	defer conn.Close()

	infoHashBytes, err := hex.DecodeString(infoHash)
	if err != nil {
		return fmt.Errorf("invalid info hash: %w", err)
	}
	payload := []byte("\x13" + "BitTorrent protocol" + "\x00\x00\x00\x00\x00\x00\x00\x00" + string(infoHashBytes) + RandomPeerId())

	_, err = conn.Write(payload)
	if err != nil {
		return fmt.Errorf("failed to send handshake: %w", err)
	}

	fmt.Println("Handshake sent: ", string(payload))

	buf := make([]byte, 102400)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	payload = []byte("\x00\x00\x00\x01\x02")
	_, err = conn.Write(payload)
	if err != nil {
		return fmt.Errorf("failed to send interested message: %w", err)
	}

	var downloadedBytes int64
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stopChan:
			log.Println("Stopping...")
			return nil

		case <-ticker.C:
			currentDownloaded := atomic.LoadInt64(&downloadedBytes)
			atomic.StoreInt64(&downloadedBytes, 0)
			currentSpeed := currentDownloaded / 1024
			atomic.StoreInt64(speed, currentSpeed)

		default:
			payload = []byte("\x00\x00\x00\x0d" + "\x06" + "\x00\x00\x00\x00" + "\x00\x00\x00\x00" + "\x00\x00\x40\x00")
			n, err = conn.Write(payload)
			if err != nil {
				return fmt.Errorf("failed to send request: %w", err)
			}

			n, err = conn.Read(buf)
			if err != nil {
				return fmt.Errorf("failed to read piece: %w", err)
			}
			atomic.AddInt64(&downloadedBytes, int64(n))
		}
	}
}
