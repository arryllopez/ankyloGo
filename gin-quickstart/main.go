package main

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func main() {
	router := gin.Default()

	router.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
		})
	})

	router.POST("/login", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "login successful",
		})
	})

	router.GET("/search", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "search completed",
		})
	})

	router.POST("/purchase", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "purchase successful",
		})
	})

	router.Run() // listen and serve on 0.0.0.0:8080
}
