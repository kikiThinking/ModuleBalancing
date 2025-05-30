/*
*

	@author: kiki
	@since: 2025/5/25
	@desc: //TODO

*
*/
package main

import (
	"ModuleBalancing/api"
	"ModuleBalancing/db"
	"ModuleBalancing/env"
	pb "ModuleBalancing/grpc"
	"errors"
	"fmt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"gopkg.in/yaml.v3"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var (
	err                   error
	service               *grpc.Server
	dbcontrol             *gorm.DB
	servicesconfiguration *env.Configuration
)

func init() {
	f, err := os.ReadFile(strings.Join([]string{"conf", "config.yaml"}, "/"))
	if err != nil {
		panic(err)
	}

	servicesconfiguration = new(env.Configuration)
	if err = yaml.Unmarshal(f, servicesconfiguration); err != nil {
		panic(err)
	}

	if servicesconfiguration.Database.Port == "" || servicesconfiguration.Database.Host == "" || servicesconfiguration.Database.Username == "" || servicesconfiguration.Database.Password == "" {
		panic("Error: The database configuration information is incomplete, please check!")
	}

	// connect db
	if dbcontrol, err = gorm.Open(mysql.Open(fmt.Sprintf(`%s:%s@tcp(%s:%s)/modulebalancing?charset=utf8mb4&parseTime=True&loc=Local`,
		servicesconfiguration.Database.Username,
		servicesconfiguration.Database.Password,
		servicesconfiguration.Database.Host,
		servicesconfiguration.Database.Port,
	)), &gorm.Config{
		Logger: logger.New(log.New(os.Stdout, "\r\n", log.Flags()), logger.Config{SlowThreshold: time.Second, LogLevel: logger.Warn, Colorful: true}),
	}); err != nil {
		panic(err)
	}

	// 设置连接池
	if dbobj, err := dbcontrol.DB(); err != nil {
		panic(err)
	} else {
		dbobj.SetMaxIdleConns(10)
		dbobj.SetMaxOpenConns(50)
		dbobj.SetConnMaxLifetime(time.Second * 30)
	}

	// 设置自动迁移模式
	if err = dbcontrol.Set("gorm:table_options", "ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_general_ci").AutoMigrate(db.AutoMigrate()...); err != nil {
		panic(err)
	}
}

func main() {
	lis, err := net.Listen("tcp", ":9999")
	if err != nil {
		panic(err)
	}

	if err = Dumpmoduletodatabse(dbcontrol); err != nil {
		panic(err)
	}

	service = grpc.NewServer()
	pb.RegisterModuleServer(service, &api.ModuleBalancing{Configuration: servicesconfiguration, Dbcontrol: dbcontrol})
	pb.RegisterExpirationpushServer(service, &api.Expirationpush{ClientList: make(map[string]chan *pb.ExpirationPushResponse), Dbcontrol: dbcontrol})
	pb.RegisterStorerecordServer(service, &api.Storerecord{Dbcontrol: dbcontrol})
	reflection.Register(service)

	log.Println("Listening on :9999")
	if err = service.Serve(lis); err != nil {
		panic(err)
	}
}

// Dumpmoduletodatabse 装载本地的Module文件, 每次程序启动时, 检查Module目录是否有新增文件
func Dumpmoduletodatabse(ctx *gorm.DB) error {
	var (
		filelist []os.DirEntry
	)

	if filelist, err = os.ReadDir(servicesconfiguration.Setting.Common); err != nil {
		return err
	}

	for _, item := range filelist {
		if item.IsDir() {
			continue
		}

		var filename string
		if dberr := ctx.Model(db.Module{}).Select(`name`).Where(db.Module{Name: item.Name()}).First(&filename).Error; dberr != nil {
			fmt.Printf("Discover new module(%s)", item.Name())
			var crc uint64
			var size int64
			if crc, size, err = env.CRC64(strings.Join([]string{servicesconfiguration.Setting.Common, item.Name()}, `\`), 128*1024*1024, 8); err != nil {
				fmt.Printf("----> Failed(%s)\r\n", err.Error())
				continue
			}

			if errors.Is(dberr, gorm.ErrRecordNotFound) {
				if err = ctx.Model(db.Module{}).Create(&db.Module{
					Name:       item.Name(),
					Size:       size,
					CRC64:      crc,
					Lastuse:    time.Now(),
					Expiration: time.Now().Add(time.Hour * 24 * time.Duration(servicesconfiguration.Setting.Expiration)),
				}).Error; err != nil {
					fmt.Printf("----> Failed(%s)\r\n", err.Error())
					continue
				}
			} else {
				fmt.Printf("----> Failed(%s)\r\n", dberr.Error())
				continue
			}
		}
	}

	return nil
}

func readrunpath() string {
	exePath, err := os.Executable()
	if err != nil {
		fmt.Println(err.Error())
	}

	res, _ := filepath.EvalSymlinks(filepath.Dir(exePath))

	dir := os.Getenv("TEMP")
	if dir == "" {
		dir = os.Getenv("TMP")
	}

	rep, _ := filepath.EvalSymlinks(dir)
	if strings.Contains(res, rep) {
		var abPath string
		_, filename, _, ok := runtime.Caller(0)
		if ok {
			abPath = path.Dir(filename)
		}
		return abPath
	}
	return res
}
