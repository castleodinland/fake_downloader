package main

import (
	"net/http"
	"sync/atomic"
	"github.com/gin-gonic/gin"
	"fake_dowloader/util"
)

var (
	stopChan     chan struct{}
	currentSpeed int64
)

func main() {
	r := gin.Default()
	r.LoadHTMLGlob("templates/*")

	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", nil)
	})

	r.POST("/start", func(c *gin.Context) {
		peerAddr := c.PostForm("peerAddr")
		infoHash := c.PostForm("infoHash")
		stopChan = make(chan struct{})

		go func() {
			util.ConnectPeerWithStop(peerAddr, infoHash, stopChan, &currentSpeed)
		}()

		c.String(http.StatusOK, "Started")
	})

	r.POST("/stop", func(c *gin.Context) {
		if stopChan != nil {
			close(stopChan)
		}
		c.String(http.StatusOK, "Stopped")
	})

	r.GET("/speed", func(c *gin.Context) {
		speed := atomic.LoadInt64(&currentSpeed)
		c.JSON(http.StatusOK, gin.H{"speed": speed})
	})

	r.Run(":8084")
}
