package trinity

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/bluele/gcache"
	"github.com/gin-gonic/gin"
	grpc_middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	"github.com/jinzhu/gorm"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var (
	// GlobalTrinity global instance
	GlobalTrinity  *Trinity
	runMode        = "Local"
	rootPath       string
	configFilePath string
)

func init() {
	rootPath, _ = os.Getwd()
	configFilePath = filepath.Join(rootPath, "config", "config.yml")
}

// Trinity struct for app subconfig
type Trinity struct {
	mu             sync.RWMutex
	runMode        string
	rootpath       string
	configFilePath string

	// COMMON
	ctx              context.Context
	setting          *Setting
	customizeSetting CustomizeSetting
	db               *gorm.DB
	vCfg             *ViewSetCfg
	logger           Logger
	cache            gcache.Cache
	serviceMesh      ServiceMesh

	// GRPC
	gServer *grpc.Server

	// HTTP
	router *gin.Engine
}

func (t *Trinity) initDefaultValue() {
	GlobalTrinity = t
}

// GetRootPath  get rootpath
func GetRootPath() string {
	return rootPath
}

// SetRootPath  get rootpath
func SetRootPath(path string) {
	rootPath = path
}

// GetConfigFilePath  get rootpath
func GetConfigFilePath() string {
	return configFilePath
}

// SetConfigFilePath  get rootpath
func SetConfigFilePath(path string) {
	configFilePath = path
}

// GetRunMode  get RunMode
func GetRunMode() string {
	return runMode
}

// SetRunMode  set RunMode
func SetRunMode(runmode string) {
	runMode = runmode
}

// New app
// initial global trinity object
func New(ctx context.Context, customizeSetting ...CustomizeSetting) *Trinity {
	t := &Trinity{
		runMode:        runMode,
		rootpath:       rootPath,
		configFilePath: configFilePath,
		ctx:            ctx,
	}
	t.mu.Lock()
	t.setting = newSetting(t.runMode, t.configFilePath).GetSetting()
	t.loadCustomizeSetting(customizeSetting...)
	t.initLogger()
	t.InitDatabase()

	switch t.setting.Webapp.Type {
	case "HTTP":
		t.initRouter()
		t.initViewSetCfg()
		break
	case "GRPC":
		t.initGRPCServer()
		break
	default:
		log.Fatal("wrong app type")
		break
	}

	if t.setting.ServiceMesh.AutoRegister {
		c, err := NewConsulRegister(
			t.setting.GetServiceMeshAddress(),
			t.setting.GetServiceMeshPort(),
			t.setting.GetProjectName(),
			t.setting.GetWebAppAddress(),
			t.setting.GetWebAppPort(),
			t.setting.GetTags(),
			t.setting.GetDeregisterAfterCritical(),
			t.setting.GetHealthCheckInterval(),
		)
		if err != nil {
			log.Fatal("get service mesh client err")
		}
		t.serviceMesh = c
	}

	t.initCache()
	t.initDefaultValue()
	t.mu.Unlock()
	return t
}

// Reload  reload trinity
func (t *Trinity) Reload(runMode string) {
	t.mu.RLock()
	t.runMode = runMode
	t.setting = newSetting(t.runMode, t.configFilePath).GetSetting()
	t.initLogger()
	t.InitDatabase()
	t.initRouter()
	t.initViewSetCfg()
	t.initDefaultValue()
	t.mu.RUnlock()
}

// reloadTrinity for reload some config
func (t *Trinity) reloadTrinity() {
	t.setting = newSetting(t.runMode, t.configFilePath).GetSetting()
	t.initLogger()
	t.InitDatabase()
	t.initRouter()
	t.initViewSetCfg()
	t.initDefaultValue()
}

// GetVCfg  get vcfg
func (t *Trinity) GetVCfg() *ViewSetCfg {
	t.mu.RLock()
	v := t.vCfg
	t.mu.RUnlock()
	return v
}

// SetVCfg  get vcfg
func (t *Trinity) SetVCfg(newVCfg *ViewSetCfg) *Trinity {
	t.mu.Lock()
	t.vCfg = newVCfg
	t.reloadTrinity()
	t.mu.Unlock()
	return t
}

func (t *Trinity) initGRPCServer() {

	cert, err := tls.LoadX509KeyPair("/Users/daniel/Documents/workspace/SolutionDelivery/conf/server/server.pem", "/Users/daniel/Documents/workspace/SolutionDelivery/conf/server/server.key")
	if err != nil {
		log.Fatalf("tls.LoadX509KeyPair err: %v", err)
	}
	certPool := x509.NewCertPool()
	ca, err := ioutil.ReadFile("/Users/daniel/Documents/workspace/SolutionDelivery/conf/ca.pem")
	if err != nil {
		log.Fatalf("ioutil.ReadFile err: %v", err)
	}
	if ok := certPool.AppendCertsFromPEM(ca); !ok {
		log.Fatalf("certPool.AppendCertsFromPEM err")
	}
	c := credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    certPool,
	})
	opts := []grpc.ServerOption{
		grpc.Creds(c),
		grpc_middleware.WithUnaryServerChain(
			RecoveryInterceptor,
			LoggingInterceptor,
		),
	}
	t.gServer = grpc.NewServer(opts...)

}

// GetGRPCServer get grpc server instance
func (t *Trinity) GetGRPCServer() *grpc.Server {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.gServer
}

// ServeGRPC serve GRPC
func (t *Trinity) ServeGRPC() {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%v", t.setting.GetWebAppPort()))
	if err != nil {
		log.Fatalf("tcp port : %v  listen err: %v", t.setting.GetWebAppPort(), err)
	}
	errors := make(chan error)
	go func() {

		if err := t.serviceMesh.RegService(); err != nil {
			errors <- err
		}
		// logger.Logger.Log("transport", "GRPC", "address", port, "msg", "listening")
		errors <- t.gServer.Serve(lis)
	}()

	go func() {
		c := make(chan os.Signal)
		signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
		errors <- fmt.Errorf("%s", <-c)
	}()

	// logger.Logger.Log("terminated", <-errors)
	fmt.Println(<-errors)
	t.serviceMesh.DeRegService()

}

// ServeHTTP serve HTTP
func (t *Trinity) ServeHTTP() {
	defer t.Close()
	s := &http.Server{
		Addr:              fmt.Sprintf(":%v", t.setting.Webapp.Port),
		Handler:           t.router,
		ReadTimeout:       time.Duration(t.setting.Webapp.ReadTimeoutSecond) * time.Second,
		ReadHeaderTimeout: time.Duration(t.setting.Webapp.ReadHeaderTimeoutSecond) * time.Second,
		WriteTimeout:      time.Duration(t.setting.Webapp.WriteTimeoutSecond) * time.Second,
		IdleTimeout:       time.Duration(t.setting.Webapp.IdleTimeoutSecond) * time.Second,
		MaxHeaderBytes:    t.setting.Webapp.MaxHeaderBytes,
	}
	t.logger.Print(fmt.Sprintf("[info] %v  start http server listening : %v, version : %v", GetCurrentTimeString(time.RFC3339), t.setting.Webapp.Port, t.setting.Version))
	s.ListenAndServe()
	return
}

// Serve http
func (t *Trinity) Serve() {
	switch t.setting.GetWebAppType() {
	case "HTTP":
		t.ServeHTTP()
		break
	case "GRPC":
		t.ServeGRPC()
		break
	default:
		log.Fatal("Unsupported Web method")
		break
	}
}

// Close http
func (t *Trinity) Close() {
	t.db.Close()
	t.logger.Print(fmt.Sprintf("[info] %v  end http server listening : %v, version : %v", GetCurrentTimeString(time.RFC3339), t.setting.Webapp.Port, t.setting.Version))
}
