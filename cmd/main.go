package main

import (
	"encoding/json"
	"strings"

	"github.com/cloudworkz/grafana-permission-sync/pkg/groups"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/yaml.v2"

	"github.com/gin-gonic/gin"
)

var (
	config    *Config
	log       *zap.SugaredLogger
	groupTree *groups.GroupTree
)

func main() {
	// 1. Setup, load config, ...
	logRaw, _ := zap.NewProduction()
	log = logRaw.Sugar()

	config = loadConfig()

	log.Infow("starting grafana syncer...",
		"grafana_url", config.Grafana.URL,
		"rules", len(config.Rules))

	// 2. Create group service
	var err error
	groupTree, err = groups.CreateGroupTree(log, config.Google.Domain, config.Google.AdminEmail, config.Google.CredentialsPath, []string{
		"https://www.googleapis.com/auth/admin.directory.group.member.readonly",
		"https://www.googleapis.com/auth/admin.directory.group.readonly",
		//"https://www.googleapis.com/auth/admin.directory.user.readonly",
	}...)
	if err != nil {
		log.Fatalw("unable to create google directory service", "error", err.Error())
	}

	// 3. Start sync
	go startSync()

	// 4. Start HTTP server
	startWebServer()
}

func startWebServer() {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()

	rawLog := log.Desugar()
	r.Use(gin.LoggerWithConfig(gin.LoggerConfig{
		Formatter: func(param gin.LogFormatterParams) string {
			// don't log liveness checks
			if strings.HasPrefix(param.Path, "/admin/") {
				return ""
			}

			fields := []zapcore.Field{
				zap.String("clientIP", param.ClientIP),
				zap.String("method", param.Method),
				zap.String("path", param.Path),
				zap.String("protocol", param.Request.Proto),
				zap.Int("statusCode", param.StatusCode),
				zap.Duration("latency", param.Latency),
				zap.String("userAgent", param.Request.UserAgent()),
			}

			if param.ErrorMessage != "" {
				fields = append(fields, zap.String("error", param.ErrorMessage))
			}

			rawLog.Info("handling http request", fields...)
			return "" // prevent unstructured logging
		},
		SkipPaths: []string{"/admin/"},
	}))

	r.Use(gin.Recovery())

	r.GET("/admin/ready", func(c *gin.Context) {
		if createdPlans > 0 {
			renderYAML(c, 200, gin.H{"status": "ready"})
		} else {
			renderYAML(c, 503, gin.H{"status": "starting"})
		}
	})

	r.GET("/admin/alive", func(c *gin.Context) {
		renderYAML(c, 200, gin.H{"status": "ready"})
	})

	r.GET("/admin/groups/:email", func(c *gin.Context) {
		email := c.Param("email")
		recurse := c.Query("recurse") == "true"
		members, err := groupTree.ListGroupMembers(email, recurse)
		if err != nil {
			renderJSON(c, 500, gin.H{"error": err.Error()})
			return
		}
		renderJSON(c, 200, members)
	})

	r.GET("/admin/users/:email", func(c *gin.Context) {
		email := c.Param("email")
		groups, err := groupTree.ListUserGroups(email)
		if err != nil {
			renderJSON(c, 500, gin.H{"error": err.Error()})
			return
		}
		renderJSON(c, 200, groups)
		// todo:
		// 1. resulting in permissions: x, y, z, ...
		// 2. and the user would not be in organizations: a, b, c, ...
	})

	err := r.Run(":3000")
	if err != nil {
		log.Fatalw("error in router.Run", "error", err)
	}
}

func renderYAML(c *gin.Context, code int, obj interface{}) {
	bytes, err := yaml.Marshal(obj)
	if err != nil {
		c.String(500, "error: "+err.Error())
	}
	c.Data(code, "text/plain; charset=utf-8", bytes)
}

func renderJSON(c *gin.Context, code int, obj interface{}) {
	bytes, err := json.MarshalIndent(obj, "", "    ")
	if err != nil {
		c.String(500, "error: "+err.Error())
	}
	c.Data(code, "text/plain; charset=utf-8", bytes)
}