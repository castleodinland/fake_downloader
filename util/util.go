
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

	log.Printf("Attempting to connect to peer: %s", peerAddr)
	
	conn, err := net.DialTimeout("tcp", peerAddr, 10*time.Second)
	if err != nil {
		log.Printf("Failed to connect to peer %s: %v", peerAddr, err)
		return fmt.Errorf("failed to connect to peer: %w", err)
	}
	defer conn.Close()

	// 设置连接超时
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	infoHashBytes, err := hex.DecodeString(infoHash)
	if err != nil {
		return fmt.Errorf("invalid info hash: %w", err)
	}
	
	if len(infoHashBytes) != 20 {
		return fmt.Errorf("info hash must be 20 bytes (40 hex characters)")
	}
	
	payload := []byte("\x13" + "BitTorrent protocol" + "\x00\x00\x00\x00\x00\x00\x00\x00" + string(infoHashBytes) + RandomPeerId())

	_, err = conn.Write(payload)
	if err != nil {
		log.Printf("Failed to send handshake: %v", err)
		return fmt.Errorf("failed to send handshake: %w", err)
	}

	log.Printf("Handshake sent to %s", peerAddr)

	buf := make([]byte, 102400)
	
	// 重置连接超时，给握手响应更多时间
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	
	n, err := conn.Read(buf)
	if err != nil {
		log.Printf("Failed to read handshake response: %v", err)
		return fmt.Errorf("failed to read response: %w", err)
	}

	log.Printf("Received handshake response: %d bytes", n)

	// 发送interested消息
	payload = []byte("\x00\x00\x00\x01\x02")
	_, err = conn.Write(payload)
	if err != nil {
		log.Printf("Failed to send interested message: %v", err)
		return fmt.Errorf("failed to send interested message: %w", err)
	}

	var downloadedBytes int64
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	// 移除总的连接超时，让连接保持活跃
	conn.SetDeadline(time.Time{})

	log.Printf("Starting download simulation for %s", peerAddr)

	for {
		select {
		case <-stopChan:
			log.Printf("Stopping connection to %s", peerAddr)
			return nil

		case <-ticker.C:
			currentDownloaded := atomic.LoadInt64(&downloadedBytes)
			atomic.StoreInt64(&downloadedBytes, 0)
			currentSpeed := currentDownloaded / 1024 // KB/s
			atomic.StoreInt64(speed, currentSpeed)

		default:
			// 设置单次操作超时
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			
			// 发送piece请求
			payload = []byte("\x00\x00\x00\x0d" + "\x06" + "\x00\x00\x00\x00" + "\x00\x00\x00\x00" + "\x00\x00\x40\x00")
			_, err = conn.Write(payload)
			if err != nil {
				log.Printf("Failed to send request to %s: %v", peerAddr, err)
				return fmt.Errorf("failed to send request: %w", err)
			}

			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			
			n, err = conn.Read(buf)
			if err != nil {
				// 如果是超时错误，继续尝试
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					log.Printf("Read timeout for %s, continuing...", peerAddr)
					continue
				}
				log.Printf("Failed to read piece from %s: %v", peerAddr, err)
				return fmt.Errorf("failed to read piece: %w", err)
			}
			
			atomic.AddInt64(&downloadedBytes, int64(n))
		}
	}
}
