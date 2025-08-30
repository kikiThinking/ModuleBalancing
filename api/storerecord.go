/*
*

	@author: kiki
	@since: 2025/6/19
	@desc: //TODO

*
*/

package api

import (
	"ModuleBalancing/db"
	"ModuleBalancing/env"
	rpc "ModuleBalancing/grpc"
	"ModuleBalancing/logmanager"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"gorm.io/gorm"
)

type Storerecord struct {
	Dbcontrol     *gorm.DB
	Configuration *env.Configuration
	Logmar        *logmanager.LogManager
	rpc.UnimplementedStorerecordServer
}

// Updatestorerecord 函数接收客户端即将使用的Module列表, 也就是AOD解析出来的结果, 服务端接收这些结果用来记录过期时间通知客户端删除
func (the *Storerecord) Updatestorerecord(stream rpc.Storerecord_UpdatestorerecordServer) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				err := stream.SendAndClose(&rpc.StorerecordResponse{})
				if err != nil {
					// 记录错误日志
					the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("Error sending and closing stream: %s", err.Error()))
				}
				return nil
			} else {
				// 记录错误日志
				the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("Error receiving stream: %s", err.Error()))
				return err
			}
		}

		// 如果心跳为空则证明服务端发送的是业务数据
		if strings.EqualFold(req.Heartbeat, "") {
			// 开启事务
			var (
				tx          = the.Dbcontrol.Begin()
				client      = new(db.Client)
				currenttime = time.Now()
			)

			if err = the.Dbcontrol.Preload(`Store`).Where(db.Client{Serveraddress: req.Serveraddress}).First(client).Error; err != nil {
				tx.Rollback() // 查询失败，回滚事务
				if errors.Is(err, gorm.ErrRecordNotFound) {
					the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("Unregistered clients(%s)", req.Serveraddress))
					return err
				} else {
					the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("failed to database error --> %s", err.Error()))
					return fmt.Errorf("failed to database error --> %s", err.Error()) // 其他error
				}
			}

			// 客户端文件超时逻辑代码
			for _, value := range req.Modulenames {
				// ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------
				// 这段代码用于在客户端更新store时同时更新服务端本地的record
				var exist = false
				if err = the.Dbcontrol.Model(db.Module{}).Select(`COUNT(*) > 0`).Where(db.Module{Name: value}).Scan(&exist).Error; err != nil {
					return err
				}

				if exist {
					if err = the.Dbcontrol.Model(db.Module{}).Where(db.Module{Name: value}).Updates(map[string]interface{}{
						`lastuse`:    currenttime.Format(`2006-01-02 15:04:05`),
						`expiration`: currenttime.Add(time.Duration(the.Configuration.Setting.Expiration) * time.Hour * 24).Format(`2006-01-02 15:04:05`),
					}).Error; err != nil {
						return err
					}
				}
				// ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------

				var clientmodule = db.Clientmodule{
					StoreID:    client.ID,
					Partnumber: req.Partnumber,
					Name:       value,
					Expiration: currenttime.Add(time.Duration(client.Maxretentiondays) * time.Hour * 24),
				}

				var isexistrecord bool
				if err = the.Dbcontrol.Unscoped().
					Model(db.Clientmodule{}).Select(`COUNT(*) > 0`).
					Where(db.Clientmodule{StoreID: clientmodule.StoreID, Name: value}).
					Scan(&isexistrecord).Error; err != nil {
					the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("Database error (%s)", err.Error()))
					return err
				}

				if isexistrecord {
					if err = the.Dbcontrol.
						Unscoped().
						Model(db.Clientmodule{}).
						Where(db.Clientmodule{StoreID: clientmodule.StoreID, Name: value}).
						Updates(map[string]interface{}{
							"partnumber": clientmodule.Partnumber,
							"expiration": clientmodule.Expiration,
							"updated_at": currenttime,
							"deleted_at": nil,
						}).Error; err != nil {
						the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("Database error (%s)", err.Error()))
						return err
					}
				} else {
					if err = the.Dbcontrol.Model(db.Clientmodule{}).Create(&clientmodule).Error; err != nil {
						the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("Database error (%s)", err.Error()))
						return err
					}
				}

				//if err = the.Dbcontrol.Clauses(
				//	clause.OnConflict{
				//		Columns: []clause.Column{{Name: "name"}, {Name: "store_id"}},
				//		DoUpdates: clause.Assignments(map[string]interface{}{
				//			"partnumber": clientmodule.Partnumber,
				//			"expiration": clientmodule.Expiration,
				//			"updated_at": gorm.Expr("CURRENT_TIMESTAMP"),
				//			"deleted_at": nil, // 明确设置为 NULL 来取消软删除
				//		}),
				//		//DoUpdates: clause.AssignmentColumns([]string{"partnumber", "expiration", "updated_at", "deleted_at"}),
				//	}).
				//	Create(&clientmodule).Error; err != nil {
				//	tx.Rollback()
				//	the.Logmar.GetLogger("Clientstore").Error(err.Error())
				//	return err
				//}

				the.Logmar.GetLogger("Clientstore").Info(fmt.Sprintf("Add client storage record --> Client(%s)  Partnumber(%s)  Filename(%s)  Expiration(%s)",
					req.Serveraddress,
					clientmodule.Partnumber,
					clientmodule.Name,
					clientmodule.Expiration.Format("2006-01-02 15:04:05"),
				))

				//found := false // 标记是否找到匹配的模块
				//for j := range client.Store {
				//	if strings.EqualFold(client.Store[j].Name, value) {
				//		found = true
				//		client.Store[j].Partnumber = req.Partnumber
				//		client.Store[j].Expiration = currenttime.Add(time.Duration(client.Maxretentiondays) * time.Hour * 24)
				//
				//		// 这里如果找到记录那就更新一下当前的AOD名称和过时间
				//		if err := tx.Model(&client.Store[j]).UpdateColumns(map[string]interface{}{
				//			"partnumber": req.Partnumber,
				//			"expiration": client.Store[j].Expiration,
				//		}).Error; err != nil {
				//			tx.Rollback()
				//			the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("failed to database error --> %s", err.Error()))
				//			return fmt.Errorf("failed to database error --> %s", err.Error()) // 事务回滚并返回错误
				//		}
				//		break // 找到匹配的模块后，跳出内层循环
				//	}
				//}
				//
				//// 没有找到对应的模块，创建新的 Clientmodule 记录
				//if !found {
				//	module := db.Clientmodule{
				//		StoreID:    client.ID,      // 关联到当前的 Client
				//		Name:       value,          // 使用 req.Modulenames 中的 value 作为 Name
				//		Partnumber: req.Partnumber, // 使用 req.Partnumber
				//		Expiration: currenttime.Add(time.Duration(client.Maxretentiondays) * time.Hour * 24),
				//	}
				//
				//	if err := tx.Create(&module).Error; err != nil {
				//		tx.Rollback()
				//		the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("failed to database error --> %s", err.Error()))
				//		return fmt.Errorf("failed to database error --> %s", err.Error()) // 事务回滚并返回错误
				//	}
				//
				//	client.Store = append(client.Store, module)
				//	the.Logmar.GetLogger("Clientstore").Info(fmt.Sprintf("Add a new client storage record --> Client(%s)  Partnumber(%s)  Filename(%s)  Expiration(%s)",
				//		req.Serveraddress,
				//		module.Partnumber,
				//		module.Name,
				//		module.Expiration.Format("2006-01-02 15:04:05"),
				//	))
				//}
			}

			if err := tx.Commit().Error; err != nil {
				tx.Rollback()
				the.Logmar.GetLogger("Clientstore").Error(fmt.Sprintf("failed to database error --> %s", err.Error()))
				return fmt.Errorf("failed to database error --> %s", err.Error()) // 事务回滚并返回错误
			}

			if r := recover(); r != nil {
				tx.Rollback()
				panic(r) // 重新抛出panic
			}
		}
	}
}
