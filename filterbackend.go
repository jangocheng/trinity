package trinity

import (
	"github.com/gin-gonic/gin"
	"github.com/jinzhu/gorm"
)

//DefaultFilterBackend for Mixin
func DefaultFilterBackend(c *gin.Context, db *gorm.DB) *gorm.DB {
	// c.GetString("reqUserKey")
	// c.GetString("UserPermission")
	return db
}
