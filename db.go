package trinity

import (
	"fmt"
	"log"
	"math/rand"
	"strings"

	"github.com/jinzhu/gorm"
	_ "github.com/lib/pq" //pg
	uuid "github.com/satori/go.uuid"
)

//InitDatabase create db connection
/**
 * initial db connection
 */
func (t *Trinity) InitDatabase() {
	var dbconnection string
	switch t.setting.GetDBType() {
	case "mysql":
		var dbconn strings.Builder
		// 向builder中写入字符 / 字符串
		dbconn.Write([]byte(t.setting.GetDBUser()))
		dbconn.WriteByte(':')
		dbconn.Write([]byte(t.setting.GetDBPassword()))
		dbconn.Write([]byte("@/"))
		dbconn.Write([]byte(t.setting.GetDBName()))
		dbconn.WriteByte('?')
		dbconn.Write([]byte(t.setting.GetDBOption()))
		dbconnection = dbconn.String()

		break
	case "postgres":
		dbconnection = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s %s",
			t.setting.GetDBHost(),
			t.setting.GetDBPort(),
			t.setting.GetDBUser(),
			t.setting.GetDBPassword(),
			t.setting.GetDBName(),
			t.setting.GetDBOption(),
		)
		break
	}
	db, err := gorm.Open(t.setting.GetDBType(), dbconnection)

	db.SetLogger(&NilLogger{})
	gorm.DefaultTableNameHandler = func(db *gorm.DB, defaultTableName string) string {
		return t.setting.GetTablePrefix() + defaultTableName
	}

	if err != nil {
		log.Fatalf("models.Setup err: %v", err)
	}
	db.LogMode(t.setting.GetDebug())
	db.SingularTable(true)
	db.Callback().Create().Replace("gorm:update_time_stamp", updateTimeStampAndUUIDForCreateCallback)
	db.Callback().Update().Replace("gorm:update_time_stamp", updateTimeStampForUpdateCallback)
	db.Callback().Delete().Replace("gorm:delete", deleteCallback)

	db.DB().SetMaxIdleConns(t.setting.GetDbMaxIdleConn())
	db.DB().SetMaxOpenConns(t.setting.GetDbMaxOpenConn())
	t.db = db

}

// GetDB  get db instance
func (t *Trinity) GetDB() *gorm.DB {
	t.mu.RLock()
	d := t.db
	t.mu.RUnlock()
	return d
}

// updateTimeStampForCreateCallback will set `CreatedOn`, `ModifiedOn` when creating
func updateTimeStampAndUUIDForCreateCallback(scope *gorm.Scope) {
	if !scope.HasError() {
		userIDInterface, _ := scope.Get("UserID")
		userID, _ := userIDInterface.(int64)
		nowTime := GetCurrentTime()
		if createTimeField, ok := scope.FieldByName("CreatedTime"); ok {
			if createTimeField.IsBlank {
				createTimeField.Set(nowTime)
			}
		}
		if createUserIDField, ok := scope.FieldByName("CreateUserID"); ok {
			if createUserIDField.IsBlank {
				createUserIDField.Set(userID)
			}
		}
		if idField, ok := scope.FieldByName("ID"); ok {
			idField.Set(GenerateSnowFlakeID(int64(rand.Intn(100))))
		}
		if modifyTimeField, ok := scope.FieldByName("UpdatedTime"); ok {
			if modifyTimeField.IsBlank {
				modifyTimeField.Set(nowTime)
			}
		}
		if updateUserIDField, ok := scope.FieldByName("UpdateUserID"); ok {
			if updateUserIDField.IsBlank {
				updateUserIDField.Set(userID)
			}
		}

		if updateDVersionField, ok := scope.FieldByName("DVersion"); ok {
			if updateDVersionField.IsBlank {
				updateDVersionField.Set(uuid.NewV4().String())
			}
		}
	}
}

// updateTimeStampForUpdateCallback will set `ModifiedOn` when updating
func updateTimeStampForUpdateCallback(scope *gorm.Scope) {
	if !scope.HasError() {
		userID, _ := scope.Get("UserID")
		var updateAttrs = map[string]interface{}{}
		if attrs, ok := scope.InstanceGet("gorm:update_attrs"); ok {
			updateAttrs = attrs.(map[string]interface{})
			updateAttrs["updated_time"] = GetCurrentTime()
			updateAttrs["update_user_id"] = userID
			updateAttrs["d_version"] = uuid.NewV4().String()
			scope.InstanceSet("gorm:update_attrs", updateAttrs)
		}
	}

}

// deleteCallback will set `DeletedOn` where deleting
func deleteCallback(scope *gorm.Scope) {
	if !scope.HasError() {
		userID, ok := scope.Get("UserID")
		if !ok {
			userID = nil
		}
		var extraOption string
		if str, ok := scope.Get("gorm:delete_option"); ok {
			extraOption = fmt.Sprint(str)
		}
		deletedAtField, hasDeletedAtField := scope.FieldByName("deleted_time")
		deleteUserIDField, hasDeleteUserIDField := scope.FieldByName("DeleteUserID")
		dVersionField, hasDVersionField := scope.FieldByName("d_version")

		if !scope.Search.Unscoped && hasDeletedAtField && hasDVersionField && hasDeleteUserIDField {
			scope.Raw(fmt.Sprintf(
				"UPDATE %v SET %v=%v,%v=%v,%v=%v%v%v",
				scope.QuotedTableName(),
				scope.Quote(deletedAtField.DBName),
				scope.AddToVars(GetCurrentTime()),
				scope.Quote(deleteUserIDField.DBName),
				scope.AddToVars(userID),
				scope.Quote(dVersionField.DBName),
				scope.AddToVars(uuid.NewV4().String()),
				addExtraSpaceIfExist(scope.CombinedConditionSql()),
				addExtraSpaceIfExist(extraOption),
			)).Exec()
		} else {
			scope.Raw(fmt.Sprintf(
				"DELETE FROM %v%v%v",
				scope.QuotedTableName(),
				addExtraSpaceIfExist(scope.CombinedConditionSql()),
				addExtraSpaceIfExist(extraOption),
			)).Exec()
		}
	}
}

// addExtraSpaceIfExist adds a separator
func addExtraSpaceIfExist(str string) string {
	if str != "" {
		return " " + str
	}
	return ""
}
