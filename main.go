package main

import (
	"net/http"
	"github.com/gin-gonic/gin"
	"dummy_pt/util"
)

var stopChan chan struct{}

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
			util.ConnectPeerWithStop(peerAddr, infoHash, stopChan)
		}()

		c.String(http.StatusOK, "Started")
	})

	r.POST("/stop", func(c *gin.Context) {
		if stopChan != nil {
			close(stopChan)
		}
		c.String(http.StatusOK, "Stopped")
	})

	r.Run(":8084")
}
