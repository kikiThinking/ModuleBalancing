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
	"ModuleBalancing/clientcontrol"
	"ModuleBalancing/db"
	"ModuleBalancing/env"
	rpc "ModuleBalancing/grpc"
	"ModuleBalancing/logmanager"
	"ModuleBalancing/route"
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/rjeczalik/notify"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"gopkg.in/yaml.v3"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	_ "net/http/pprof"
)

var (
	err                     error
	rpcservice              *grpc.Server
	dbcontrol               *gorm.DB
	servicesconfiguration   *env.Configuration
	logmar                  = logmanager.InitManager()
	clientexpirationchannel = make(map[string]chan *rpc.ExpirationPushResponse, 100)
	clientupdatecontrol     *clientcontrol.ClientControl
)

func init() {
	Programinformation()
	fmt.Println(readrunpath())
	f, err := os.ReadFile(strings.Join([]string{readrunpath(), "conf", "config.yaml"}, "/"))
	if err != nil {
		panic(err)
	}

	// 初始化日志
	logmar.RegisterBusiness(logmanager.LoggerConfig{
		BusinessName: "Database",
		LogDir:       fmt.Sprintf(strings.Join([]string{readrunpath(), "logs", "db"}, `\`)),
		MaxSize:      1,
		MaxBackups:   90,
		MinLevel:     logmanager.INFO,
	})

	// 初始化日志
	logmar.RegisterBusiness(logmanager.LoggerConfig{
		BusinessName: "Clientstore",
		LogDir:       fmt.Sprintf(strings.Join([]string{readrunpath(), "logs", "clientstore"}, `\`)),
		MaxSize:      1,
		MaxBackups:   90,
		MinLevel:     logmanager.INFO,
	})

	// 初始化日志
	logmar.RegisterBusiness(logmanager.LoggerConfig{
		BusinessName: "Expiration",
		LogDir:       fmt.Sprintf(strings.Join([]string{readrunpath(), "logs", "expiration"}, `\`)),
		MaxSize:      1,
		MaxBackups:   90,
		MinLevel:     logmanager.INFO,
	})

	// 初始化日志
	logmar.RegisterBusiness(logmanager.LoggerConfig{
		BusinessName: "Download",
		LogDir:       fmt.Sprintf(strings.Join([]string{readrunpath(), "logs", "dl"}, `\`)),
		MaxSize:      1,
		MaxBackups:   90,
		MinLevel:     logmanager.INFO,
	})

	// 初始化日志
	logmar.RegisterBusiness(logmanager.LoggerConfig{
		BusinessName: "Monitornewmodule",
		LogDir:       fmt.Sprintf(strings.Join([]string{readrunpath(), "logs", "monitor"}, `\`)),
		MaxSize:      1,
		MaxBackups:   90,
		MinLevel:     logmanager.INFO,
	})

	// 初始化日志
	logmar.RegisterBusiness(logmanager.LoggerConfig{
		BusinessName: "Upload",
		LogDir:       fmt.Sprintf(strings.Join([]string{readrunpath(), "logs", "upload"}, `\`)),
		MaxSize:      1,
		MaxBackups:   90,
		MinLevel:     logmanager.INFO,
	})

	// 初始化日志
	logmar.RegisterBusiness(logmanager.LoggerConfig{
		BusinessName: "Dumplocalmodules",
		LogDir:       fmt.Sprintf(strings.Join([]string{readrunpath(), "logs", "dump"}, `\`)),
		MaxSize:      1,
		MaxBackups:   90,
		MinLevel:     logmanager.INFO,
	})

	// 初始化日志
	logmar.RegisterBusiness(logmanager.LoggerConfig{
		BusinessName: "Expirationforclient",
		LogDir:       fmt.Sprintf(strings.Join([]string{readrunpath(), "logs", "expirationforclient"}, `\`)),
		MaxSize:      1,
		MaxBackups:   90,
		MinLevel:     logmanager.INFO,
	})

	logmar.RegisterBusiness(logmanager.LoggerConfig{
		BusinessName: "IntegrityVerification",
		LogDir:       fmt.Sprintf(strings.Join([]string{readrunpath(), "logs", "integrityverification"}, `\`)),
		MaxSize:      1,
		MaxBackups:   90,
		MinLevel:     logmanager.INFO,
	})

	logmar.RegisterBusiness(logmanager.LoggerConfig{
		BusinessName: "Analyzing",
		LogDir:       fmt.Sprintf(strings.Join([]string{readrunpath(), "logs", "analyzing"}, `\`)),
		MaxSize:      1,
		MaxBackups:   90,
		MinLevel:     logmanager.INFO,
	})

	logmar.RegisterBusiness(logmanager.LoggerConfig{
		BusinessName: "Unwanted",
		LogDir:       fmt.Sprintf(strings.Join([]string{readrunpath(), "logs", "unwanted"}, `\`)),
		MaxSize:      1,
		MaxBackups:   90,
		MinLevel:     logmanager.INFO,
	})

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

	if clientupdatecontrol, err = clientcontrol.New(readrunpath()); err != nil {
		panic(err)
	}
}

func main() {
	gin.SetMode(gin.ReleaseMode)
	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", servicesconfiguration.GRPC.Port))
	if err != nil {
		panic(err)
	}

	go hotloadding(strings.Join([]string{readrunpath(), "conf"}, `\`))
	// 启动时装载本地没有记录的Module
	//if err = Dumpmoduletodatabse(dbcontrol); err != nil {
	//	panic(err)
	//}

	// 实时监听Module目录, 当Module目录新增Module时
	go env.Monitornewmodule(dbcontrol, logmar.GetLogger("Monitornewmodule"), servicesconfiguration.Setting.Expiration, servicesconfiguration.Setting.Common)

	// 过期Module备份和删除
	//go expirationcheck(dbcontrol)
	//go clientexpirationcheck(dbcontrol)
	//go Removeunwantedrecord(dbcontrol)

	// 监听客户端文件变化
	go clientupdatecontrol.Monitor()

	fmt.Println(clientupdatecontrol.MD5())
	rpcservice = grpc.NewServer()
	rpc.RegisterClientCheckServer(rpcservice, &api.ClientCheck{ClientUpdateControl: clientupdatecontrol})
	rpc.RegisterModuleServer(rpcservice, &api.ModuleBalancing{Configuration: servicesconfiguration, Dbcontrol: dbcontrol, Logmar: logmar})
	rpc.RegisterExpirationpushServer(rpcservice, &api.Expirationpush{ClientList: clientexpirationchannel, Dbcontrol: dbcontrol, Logmar: logmar})
	rpc.RegisterStorerecordServer(rpcservice, &api.Storerecord{Dbcontrol: dbcontrol, Configuration: servicesconfiguration, Logmar: logmar})
	reflection.Register(rpcservice)

	// 多路复用的实现 HTTP1.1|HTTP2(GRPC)
	var server = http.Server{
		Handler: h2c.NewHandler(MixedHandler(rpcservice, SetupGinServer()), &http2.Server{}), //
	}

	fmt.Printf("Listening on :%s\r\n", servicesconfiguration.GRPC.Port)
	if err = server.Serve(lis); err != nil {
		panic(err)
	}
}

func SetupGinServer() *gin.Engine {
	r := gin.Default()
	r.Use(func(ctx *gin.Context) {
		ctx.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		ctx.Writer.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE, UPDATE")
		ctx.Writer.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Accept, Authorization, X-CSRF-Token")
		ctx.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
		if ctx.Request.Method == "OPTIONS" {
			ctx.AbortWithStatus(200)
		} else {
			ctx.Next()
		}
	})

	r.LoadHTMLGlob(fmt.Sprintf("%s/web/*.html", readrunpath()))

	r.GET("/index", func(ctx *gin.Context) {
		ctx.HTML(http.StatusOK, "index.html", gin.H{"Title": "module balancing"})
	})

	r.StaticFS("/static", http.Dir(fmt.Sprintf("%s/web", readrunpath())))

	var routes = route.GinRoutes{
		Logwri:      nil,
		DBConnect:   dbcontrol,
		Serverstart: time.Now().Format("2006-01-02 15:04:05"),
	}

	return routes.InformationCollection(r)
}

func MixedHandler(grpcServer *grpc.Server, ginRouter *gin.Engine) http.Handler {
	ginHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ginRouter.ServeHTTP(w, r)
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isGRPCRequest(r) {
			fmt.Printf("[GRPC] %s\t%s\r\n", time.Now().Format(`2006/01/02 - 15:04:05`), strings.ToUpper(r.URL.Path))
			grpcServer.ServeHTTP(w, r)
			return
		}

		ginHandler.ServeHTTP(w, r)
	})
}

func isGRPCRequest(r *http.Request) bool {
	// 方法 1: 检查 Content-Type
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
		return true
	}

	// 方法 2: 检查 HTTP/2 和路径特征
	if r.ProtoMajor == 2 && strings.Contains(r.URL.Path, ".") {
		return true
	}

	// 方法 3: 检查用户代理（可选）
	if strings.Contains(r.Header.Get("User-Agent"), "grpc-") {
		return true
	}

	return false
}

// Dumpmoduletodatabse 装载本地的Module文件, 每次程序启动时, 检查Module目录是否有新增文件
func Dumpmoduletodatabse(ctx *gorm.DB) error {
	var (
		filelist []os.DirEntry
	)

	if filelist, err = os.ReadDir(servicesconfiguration.Setting.Common); err != nil {
		return err
	}

	logmar.GetLogger("Dumplocalmodules").Info(fmt.Sprintf("Check Local Module(%v)", len(filelist)))
	for index, item := range filelist {
		if item.IsDir() {
			continue
		}

		var exist bool
		if err = ctx.Model(db.Module{}).Select(`COUNT(*) > 0`).Where(db.Module{Name: item.Name()}).Scan(&exist).Error; err != nil {
			logmar.GetLogger("Dumplocalmodules").Error(fmt.Sprintf("Database error (%s)", err.Error()))
			return err
		}

		if !exist {
			logmar.GetLogger("Dumplocalmodules").Info(fmt.Sprintf("(%v)Discover new module(%s)", index, item.Name()))
			fmt.Printf("(%v)Discover new module(%s)", index, item.Name())

			var crc uint64
			var size int64
			if crc, size, err = env.CRC64(strings.Join([]string{servicesconfiguration.Setting.Common, item.Name()}, `\`), 128*1024*1024, 8); err != nil {
				logmar.GetLogger("Dumplocalmodules").Error(fmt.Sprintf("----> Failed(%s)", err.Error()))
				fmt.Printf("----> Failed(%s)\r\n", err.Error())
				continue
			}

			var module = db.Module{
				CRC64:      crc,
				Name:       item.Name(),
				Size:       size,
				Lastuse:    time.Now(),
				Expiration: time.Now().Add(time.Hour * 24 * time.Duration(servicesconfiguration.Setting.Expiration)),
			}

			var isexistrecord bool
			if err = dbcontrol.Unscoped().
				Model(db.Module{}).Select(`COUNT(*) > 0`).
				Where(db.Module{Name: module.Name}).
				Scan(&isexistrecord).Error; err != nil {
				return err
			}

			if isexistrecord {
				if err = dbcontrol.
					Unscoped().
					Model(db.Module{}).
					Where(db.Module{Name: module.Name}).
					Updates(map[string]interface{}{
						"crc64":      module.CRC64,
						"size":       module.Size,
						"lastuse":    module.Lastuse,
						"expiration": module.Expiration,
						"deleted_at": nil,
					}).Error; err != nil {
					logmar.GetLogger("Dumplocalmodules").Error(fmt.Sprintf("----> Failed(%s)", err.Error()))
					return err
				}
			} else {
				if err = dbcontrol.Model(db.Module{}).Create(&module).Error; err != nil {
					logmar.GetLogger("Dumplocalmodules").Error(fmt.Sprintf("----> Failed(%s)", err.Error()))
					return err
				}
			}

			//if err = ctx.Clauses(
			//	clause.OnConflict{
			//		Columns:   []clause.Column{{Name: "name"}},
			//		DoUpdates: clause.AssignmentColumns([]string{"crc64", "size", "lastuse", "expiration", "deleted_at"}),
			//	}).Create(&db.Module{
			//	Name:       item.Name(),
			//	Size:       size,
			//	CRC64:      crc,
			//	Lastuse:    time.Now(),
			//	Expiration: time.Now().Add(time.Hour * 24 * time.Duration(servicesconfiguration.Setting.Expiration)),
			//}).Error; err != nil {
			//	logmar.GetLogger("Dumplocalmodules").Error(fmt.Sprintf("----> Failed(%s)", err.Error()))
			//	fmt.Printf("----> Failed(%s)\r\n", err.Error())
			//	continue
			//}

			logmar.GetLogger("Dumplocalmodules").Info("----> OK")
			fmt.Println("----> OK")
		}
	}

	return nil
}

// hotloadding 热重载函数 重载config
func hotloadding(fp string) {
	var (
		monitorchange = make(chan notify.EventInfo, 10)
	)

	if err = notify.Watch(fp, monitorchange, notify.FileNotifyChangeLastWrite); err != nil {
		panic(err)
	}

	for {
		select {
		case finfo := <-monitorchange:
			switch strings.ToUpper(filepath.Base(finfo.Path())) {
			case "CONFIG.YAML":
				f, err := os.ReadFile(finfo.Path())
				if err != nil {
					fmt.Println("Error: failed to read config file:", finfo.Path())
					continue
				}

				if err = yaml.Unmarshal(f, &servicesconfiguration); err != nil {
					fmt.Println("Error: failed to unmarshal config file:", finfo.Path())
					continue
				}

				fmt.Println("services configuration reload successful")
			}
		}
	}
}

// expirationcheck 过期Module的检查, 备份, 移除数据库记录已经本地文件
func expirationcheck(ctx *gorm.DB) {
	var ticker = time.NewTicker(time.Duration(servicesconfiguration.Setting.CheckExpiration) * time.Minute)
	for range ticker.C {
		var expirationlist = make([]db.Module, 0)
		// 查询已经过期的Module文件
		if err = ctx.Model(db.Module{}).Where(`expiration <?`, time.Now()).Find(&expirationlist).Error; err != nil {
			logmar.GetLogger("Expiration").Error(fmt.Sprintf("Failed to Query Expiration Module(%s)", err.Error()))
			continue
		}

		if len(expirationlist) == 0 {
			logmar.GetLogger("Expiration").Info("Expiration Module is empty!")
			continue
		}

		logmar.GetLogger("Expiration").Info(fmt.Sprintf("Expiration Module(%v)", len(expirationlist)))
		for index, item := range expirationlist {
			logmar.GetLogger("Expiration").Info(fmt.Sprintf("(%v)Backup Module files ----> %s", index+1, item.Name))
			backcontext, cancel := context.WithCancel(context.Background())

			if err = env.Uploadtoback(
				backcontext,
				fmt.Sprintf("%s:%s", servicesconfiguration.Backup.Host, servicesconfiguration.Backup.Port),
				servicesconfiguration.Setting.Common,
				item,
				logmar.GetLogger("Expiration"),
			); err != nil {
				cancel()
				logmar.GetLogger("Expiration").Error(fmt.Sprintf("Failed to Backup Module (%s)", err.Error()))
				continue
			}

			logmar.GetLogger("Expiration").Info(fmt.Sprintf("Backup Module (%s) is successfully", item.Name))
			cancel()

			if err = ctx.Where(`id =?`, item.ID).Delete(&db.Module{}).Error; err != nil {
				logmar.GetLogger("Expiration").Error(fmt.Sprintf("Failed to deleted database record(%s)", err.Error()))
				continue
			}

			if err = os.Remove(strings.Join([]string{servicesconfiguration.Setting.Common, item.Name}, `\`)); err != nil {
				logmar.GetLogger("Expiration").Error(fmt.Sprintf("Failed to remove local file(%s)", err.Error()))
				continue
			}

			logmar.GetLogger("Expiration").Info("Deleted database record is successfully")
		}
	}
}

func Removeunwantedrecord(ctx *gorm.DB) {
	var ticker = time.NewTicker(time.Duration(servicesconfiguration.Setting.CheckUnwanted) * time.Minute)
	for range ticker.C {
		var modelrecord = make([]db.Module, 0)
		if err = ctx.Find(&modelrecord).Error; err != nil {
			logmar.GetLogger("Unwanted").Error(err.Error())
			continue
		}

		logmar.GetLogger("Unwanted").Info(fmt.Sprintf("The file does not exist, the database record has been deleted(%d)", len(modelrecord)))
		for _, value := range modelrecord {
			if _, err = os.Stat(strings.Join([]string{servicesconfiguration.Setting.Common, value.Name}, `\`)); os.IsNotExist(err) {
				if err = ctx.Where(`id =?`, value.ID).Delete(&db.Module{}).Error; err != nil {
					logmar.GetLogger("Unwanted").Error(err.Error())
					continue
				}
				logmar.GetLogger("Unwanted").Info(fmt.Sprintf("%s  ----> OK", value.Name))
			}
		}
	}
}

// clientexpirationcheck 检查客户端的Module过期时间
func clientexpirationcheck(ctx *gorm.DB) {
	var ticker = time.NewTicker(time.Duration(servicesconfiguration.Setting.CheckClientExpiration) * time.Minute)
	for range ticker.C {
		var clientlist = make([]db.Client, 0)

		if err = ctx.Preload(`Store`).Find(&clientlist).Error; err != nil {
			logmar.GetLogger("Expirationforclient").Error("Failed to query clientcontrol record")
			continue
		}

		if len(clientlist) == 0 {
			logmar.GetLogger("Expirationforclient").Error(fmt.Sprintf("There is no clientcontrol connention recoed!"))
		}

		// 这里是客户端变更过期时间后更新已知Module过期时间的关键代码
		var updated = false
		for _, client := range clientlist {
			if !client.Reload {
				logmar.GetLogger("Expirationforclient").Error(fmt.Sprintf("Update cliect expiration to(%dDay)", client.Maxretentiondays))
				for _, item := range client.Store {
					updated = true
					if err = ctx.Model(db.Clientmodule{}).Where(`id = ?`, item.ID).Update(`expiration`, item.CreatedAt.Add(time.Duration(client.Maxretentiondays)*time.Hour*24)).Error; err != nil {
						logmar.GetLogger("Expirationforclient").Error(fmt.Sprintf("Failed to update cliect expiration to(%dDay)", client.Maxretentiondays))
						continue
					}
				}

				if err = ctx.Model(db.Client{}).Where(db.Client{Serveraddress: client.Serveraddress}).Update(`reload`, true).Error; err != nil {
					logmar.GetLogger("Expirationforclient").Error(fmt.Sprintf("Failed to update clientcontrol reload status(%s)", err.Error()))
				}
			}
		}

		if updated {
			if err = ctx.Preload(`Store`).Find(&clientlist).Error; err != nil {
				logmar.GetLogger("Expirationforclient").Error("Failed to query clientcontrol record")
				continue
			}
		}

		logmar.GetLogger("Expirationforclient").Info(fmt.Sprintf("Check clientcontrol(%v)", len(clientlist)))
		for index, item := range clientlist {
			if _, ok := clientexpirationchannel[item.Serveraddress]; !ok {
				logmar.GetLogger("Expirationforclient").Info(fmt.Sprintf("(%v)Client address (%s) is offline, skip checked", index, item.Serveraddress))
				continue
			}

			logmar.GetLogger("Expirationforclient").Info(fmt.Sprintf("(%v)Client address (%s)", index, item.Serveraddress))
			var expirationlist = make(map[string]*rpc.ExpirationPushResponse)
			for _, val := range item.Store {
				if time.Now().After(val.Expiration) {
					if err = ctx.Where(`id =?`, val.ID).Delete(&db.Clientmodule{}).Error; err != nil {
						logmar.GetLogger("Expirationforclient").Error(fmt.Sprintf("Failed to delete database recoed(%s)", val.Name))
						continue
					}

					logmar.GetLogger("Expirationforclient").Info(fmt.Sprintf("Add Expiration Module Partnumber: %s Modulename: %s", val.Partnumber, val.Name))

					var normalexist bool
					if err = ctx.Model(db.Normal{}).Select(`COUNT(*) > 0`).Where(db.Normal{Name: val.Name}).Scan(&normalexist).Error; err != nil {
						logmar.GetLogger("Expirationforclient").Error(fmt.Sprintf("Check Normal Module(%s)", val.Name))
					}

					if normalexist {
						logmar.GetLogger("Expirationforclient").Info(fmt.Sprintf("Module(%s) and Normal preload share. skip", val.Name))
						continue
					}

					if _, exist := expirationlist[val.Partnumber]; exist {
						expirationlist[val.Partnumber].Modulename = append(expirationlist[val.Partnumber].Modulename, val.Name)
					} else {
						expirationlist[val.Partnumber] = new(rpc.ExpirationPushResponse)
						expirationlist[val.Partnumber] = &rpc.ExpirationPushResponse{
							Partnumber: val.Partnumber,
							Modulename: []string{val.Name},
							Heartbeat:  "",
						}
					}
				}
			}

			if len(expirationlist) > 0 {
				// 通知客户端删除已经过期的Module
				for _, val := range expirationlist {
					clientexpirationchannel[item.Serveraddress] <- val
				}
			}
		}
	}
}

// readrunpath 获取程序运行路径
func readrunpath() string {
	exePath, err := os.Executable()
	if err == nil {
		// 解析符号链接
		if realPath, err := filepath.EvalSymlinks(exePath); err == nil {
			exePath = realPath
		}

		// 检查是否为go run临时文件
		tempDir := filepath.ToSlash(os.TempDir())
		absPath := filepath.ToSlash(exePath)
		if !strings.Contains(absPath, tempDir) ||
			(!strings.Contains(absPath, "go-build") && (!strings.Contains(absPath, "go-run"))) {
			return filepath.Dir(exePath)
		}
	}

	// 2. 尝试通过调用栈获取项目根目录
	_, filename, _, ok := runtime.Caller(0)
	if ok {
		currentDir := filepath.Dir(filename)
		// 向上查找项目特征文件
		for depth := 0; depth < 10; depth++ {
			// 检查项目特征文件
			checks := []string{"go.mod", ".git", "main.go"}
			for _, check := range checks {
				if _, err := os.Stat(filepath.Join(currentDir, check)); err == nil {
					return currentDir
				}
			}

			// 向上一级目录
			parent := filepath.Dir(currentDir)
			if parent == currentDir {
				break
			}
			currentDir = parent
		}
	}

	// 3. 回退到当前工作目录
	if wd, err := os.Getwd(); err == nil {
		return wd
	}

	// 4. 最终回退
	return "."
}

func Programinformation() {
	var programname = `

███╗   ███╗ ██████╗ ██████╗ ██╗   ██╗██╗     ███████╗██████╗  █████╗ ██╗      █████╗ ███╗   ██╗ ██████╗██╗███╗   ██╗ ██████╗       ███████╗
████╗ ████║██╔═══██╗██╔══██╗██║   ██║██║     ██╔════╝██╔══██╗██╔══██╗██║     ██╔══██╗████╗  ██║██╔════╝██║████╗  ██║██╔════╝       ██╔════╝
██╔████╔██║██║   ██║██║  ██║██║   ██║██║     █████╗  ██████╔╝███████║██║     ███████║██╔██╗ ██║██║     ██║██╔██╗ ██║██║  ███╗█████╗███████╗
██║╚██╔╝██║██║   ██║██║  ██║██║   ██║██║     ██╔══╝  ██╔══██╗██╔══██║██║     ██╔══██║██║╚██╗██║██║     ██║██║╚██╗██║██║   ██║╚════╝╚════██║
██║ ╚═╝ ██║╚██████╔╝██████╔╝╚██████╔╝███████╗███████╗██████╔╝██║  ██║███████╗██║  ██║██║ ╚████║╚██████╗██║██║ ╚████║╚██████╔╝      ███████║
╚═╝     ╚═╝ ╚═════╝ ╚═════╝  ╚═════╝ ╚══════╝╚══════╝╚═════╝ ╚═╝  ╚═╝╚══════╝╚═╝  ╚═╝╚═╝  ╚═══╝ ╚═════╝╚═╝╚═╝  ╚═══╝ ╚═════╝       ╚══════╝
                                                                                                                                           
`

	fmt.Printf("%s\r\n\r\n", programname)
}
