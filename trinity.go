package trinity

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
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
	GlobalTrinity *Trinity
	// DefaultWriter  Default Writer
	defaultWriter  io.Writer
	runMode        = "Local"
	rootPath, _    = os.Getwd()
	configFilePath = filepath.Join(rootPath, "config", "config.yml")
)

// SetRunMode  set RunMode
func SetRunMode(runmode string) {
	runMode = runmode
}

// SetDefaultWriter set default writer for trinity
func SetDefaultWriter(writer io.Writer) {
	defaultWriter = writer
	gin.DefaultWriter = writer

}

// SetConfigFilePath  get rootpath
func SetConfigFilePath(path string) {
	configFilePath = path
}

// Trinity struct for app subconfig
type Trinity struct {
	mu      sync.RWMutex
	runMode string

	// COMMON
	setting          ISetting
	customizeSetting CustomizeSetting
	db               *gorm.DB
	vCfg             *ViewSetCfg
	logger           Logger
	cache            gcache.Cache
	serviceMesh      ServiceMesh

	srvPool  map[string]*sync.Pool
	repoPool map[string]*sync.Pool

	// GRPC
	gServer *grpc.Server

	// HTTP
	router *gin.Engine
}

// will be removed
func (t *Trinity) initDefaultValue() {
	GlobalTrinity = t
}

// New app
// initial global trinity object
func New(customizeSetting ...CustomizeSetting) *Trinity {
	t := &Trinity{
		runMode: runMode,
	}
	t.mu.Lock()
	t.setting = newSetting(t.runMode, configFilePath).GetSetting()

	if defaultWriter == nil {
		defaultWriter = initLoggerWriter(t.setting)
	}
	t.logger = NewDefaultLogger(NewUserRequestsCtx(nil), t.setting)
	t.InitDatabase()
	t.db.SetLogger(t.logger)

	switch t.setting.GetWebAppType() {
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

	if t.setting.GetServiceMeshAutoRegister() {
		switch t.setting.GetServiceMeshType() {
		case "consul":
			c, err := NewConsulRegister(
				t.setting.GetServiceMeshAddress(),
				t.setting.GetServiceMeshPort(),
			)
			if err != nil {
				log.Fatal("get service mesh client err")
			}
			t.serviceMesh = c
			break
		case "etcd":
			c, err := NewEtcdRegister(
				t.setting.GetServiceMeshAddress(),
				t.setting.GetServiceMeshPort(),
			)
			if err != nil {
				log.Fatal("get service mesh client err")
			}
			t.serviceMesh = c
			break
		default:
			log.Fatal("wrong service mash type")
		}

	}

	t.initCache()
	t.initDefaultValue()
	t.mu.Unlock()
	LoadCustomizeSetting(customizeSetting...)
	return t
}

// GetVCfg  get vcfg
func (t *Trinity) GetVCfg() *ViewSetCfg {
	t.mu.RLock()
	v := t.vCfg
	t.mu.RUnlock()
	return v
}

// GetLogger  get vcfg
func (t *Trinity) GetLogger() Logger {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.logger

}

func (t *Trinity) initGRPCServer() {

	if t.setting.GetTLSEnabled() {
		cert, err := tls.LoadX509KeyPair(t.setting.GetServerPemFile(), t.setting.GetServerKeyFile())
		if err != nil {
			log.Fatalf("tls.LoadX509KeyPair err: %v", err)
		}
		certPool := x509.NewCertPool()
		ca, err := ioutil.ReadFile(t.setting.GetCAPemFile())
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
				RecoveryInterceptor(t.logger),
				LoggingInterceptor(t.logger, t.setting),
				UserAuthInterceptor(t.logger),
			),
		}
		t.gServer = grpc.NewServer(opts...)
	} else {
		opts := []grpc.ServerOption{
			grpc_middleware.WithUnaryServerChain(
				RecoveryInterceptor(t.logger),
				LoggingInterceptor(t.logger, t.setting),
				UserAuthInterceptor(t.logger),
			),
		}
		t.gServer = grpc.NewServer(opts...)
	}

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
	gErr := make(chan error)
	go func() {

		if err := t.serviceMesh.RegService(
			t.setting.GetProjectName(),
			t.setting.GetProjectVersion(),
			t.setting.GetWebAppAddress(),
			t.setting.GetWebAppPort(),
			t.setting.GetTags(),
			t.setting.GetDeregisterAfterCritical(),
			t.setting.GetHealthCheckInterval(),
			t.setting.GetTLSEnabled(),
		); err != nil {
			gErr <- err
		}
		// logger.Logger.Log("transport", "GRPC", "address", port, "msg", "listening")
		t.logger.Print(fmt.Sprintf("[info] %v  start GRPC server listening : %v, version : %v", GetCurrentTimeString(time.RFC3339), t.setting.GetWebAppPort(), t.setting.GetProjectVersion()))
		gErr <- t.gServer.Serve(lis)
	}()

	go func() {
		c := make(chan os.Signal)
		signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
		gErr <- fmt.Errorf("%s", <-c)
	}()
	t.logger.Print(fmt.Sprintf("[info] %v   GRPC server listening ended : %v, version : %v , %v ", GetCurrentTimeString(time.RFC3339), t.setting.GetWebAppPort(), t.setting.GetProjectVersion(), <-gErr))
	t.serviceMesh.DeRegService(
		t.setting.GetProjectName(),
		t.setting.GetProjectVersion(),
		t.setting.GetWebAppAddress(),
		t.setting.GetWebAppPort(),
	)

}

// ServeHTTP serve HTTP
func (t *Trinity) ServeHTTP() {
	defer t.Close()
	s := &http.Server{
		Addr:              fmt.Sprintf(":%v", t.setting.GetWebAppPort()),
		Handler:           t.router,
		ReadTimeout:       time.Duration(t.setting.GetReadTimeoutSecond()) * time.Second,
		ReadHeaderTimeout: time.Duration(t.setting.GetReadHeaderTimeoutSecond()) * time.Second,
		WriteTimeout:      time.Duration(t.setting.GetWriteTimeoutSecond()) * time.Second,
		IdleTimeout:       time.Duration(t.setting.GetIdleTimeoutSecond()) * time.Second,
		MaxHeaderBytes:    t.setting.GetMaxHeaderBytes(),
	}
	t.logger.Print(fmt.Sprintf("[info] %v  start http server listening : %v, version : %v", GetCurrentTimeString(time.RFC3339), t.setting.GetWebAppPort(), t.setting.GetProjectVersion()))
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
	t.logger.Print(fmt.Sprintf("[info] %v  end http server listening : %v, version : %v", GetCurrentTimeString(time.RFC3339), t.setting.GetWebAppPort(), t.setting.GetProjectVersion()))
}
