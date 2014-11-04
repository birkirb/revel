package revel

import (
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rcrowley/goagain"

	"code.google.com/p/go.net/websocket"
)

var (
	MainRouter         *Router
	MainTemplateLoader *TemplateLoader
	MainWatcher        *Watcher
	Server             *http.Server
	wg                 sync.WaitGroup
)

// This method handles all requests.  It dispatches to handleInternal after
// handling / adapting websocket connections.
func handle(w http.ResponseWriter, r *http.Request) {
	upgrade := r.Header.Get("Upgrade")
	if upgrade == "websocket" || upgrade == "Websocket" {
		websocket.Handler(func(ws *websocket.Conn) {
			r.Method = "WS"
			handleInternal(w, r, ws)
		}).ServeHTTP(w, r)
	} else {
		handleInternal(w, r, nil)
	}
}

func handleInternal(w http.ResponseWriter, r *http.Request, ws *websocket.Conn) {
	wg.Add(1)
	defer wg.Done()
	var (
		req  = NewRequest(r)
		resp = NewResponse(w)
		c    = NewController(req, resp)
	)
	req.Websocket = ws

	Filters[0](c, Filters[1:])
	if c.Result != nil {
		c.Result.Apply(req, resp)
	} else if c.Response.Status != 0 {
		c.Response.Out.WriteHeader(c.Response.Status)
	}
	// Close the Writer if we can
	if w, ok := resp.Out.(io.Closer); ok {
		w.Close()
	}
}

// Run the server.
// This is called from the generated main file.
// If port is non-zero, use that.  Else, read the port from app.conf.
func Run(port int) {
	address := HttpAddr
	if port == 0 {
		port = HttpPort
	}

	var network = "tcp"
	var localAddress string

	// If the port is zero, treat the address as a fully qualified local address.
	// This address must be prefixed with the network type followed by a colon,
	// e.g. unix:/tmp/app.socket or tcp6:::1 (equivalent to tcp6:0:0:0:0:0:0:0:1)
	if port == 0 {
		parts := strings.SplitN(address, ":", 2)
		network = parts[0]
		localAddress = parts[1]
	} else {
		localAddress = address + ":" + strconv.Itoa(port)
	}

	MainTemplateLoader = NewTemplateLoader(TemplatePaths)

	// The "watch" config variable can turn on and off all watching.
	// (As a convenient way to control it all together.)
	if Config.BoolDefault("watch", true) {
		MainWatcher = NewWatcher()
		Filters = append([]Filter{WatchFilter}, Filters...)
	}

	// If desired (or by default), create a watcher for templates and routes.
	// The watcher calls Refresh() on things on the first request.
	if MainWatcher != nil && Config.BoolDefault("watch.templates", true) {
		MainWatcher.Listen(MainTemplateLoader, MainTemplateLoader.paths...)
	} else {
		MainTemplateLoader.Refresh()
	}

	Server = &http.Server{
		Addr:    localAddress,
		Handler: http.HandlerFunc(handle),
	}

	runStartupHooks()

	listener, err := goagain.Listener()
	if nil != err {
		go func() {
			time.Sleep(100 * time.Millisecond)
			INFO.Printf("Listening on %s...\n", localAddress)
		}()

		listener, err = net.Listen(network, localAddress)
		if err != nil {
			ERROR.Fatalln("Failed to listen:", err)
			return
		}

		go startServe(network, localAddress, listener)
	} else {
		go func() {
			time.Sleep(100 * time.Millisecond)
			INFO.Printf("Resuming Listening on %s...\n", localAddress)
		}()

		go startServe(network, localAddress, listener)

		// Kill the parent, now that the child has started successfully.
		if err := goagain.Kill(); nil != err {
			ERROR.Fatalln(err)
		}
	}

	INFO.Printf("Monitoring signals.\n")

	// Block the main goroutine awaiting signals.
	if _, err := goagain.Wait(listener); nil != err {
		ERROR.Fatalln(err)
	}

	INFO.Printf("Closing listener.\n")

	// Close the listener so we stop accepting new requests.
	// Existing ones should still be completed.
	if err := listener.Close(); nil != err {
		ERROR.Fatalln(err)
	}

	INFO.Printf("Waiting for handlers to complete.\n")
	wg.Wait()

	INFO.Printf("Running Shutdown Hooks..\n")
	runShutdownHooks()

	INFO.Printf("Exit.\n")
}

func startServe(network string, localAddress string, listener net.Listener) {
	ERROR.Fatalln("Failed to serve:", Server.Serve(listener))
	// if HttpSsl {
	// 	if network != "tcp" {
	// 		// This limitation is just to reduce complexity, since it is standard
	// 		// to terminate SSL upstream when using unix domain sockets.
	// 		ERROR.Fatalln("SSL is only supported for TCP sockets. Specify a port to listen on.")
	// 	}
	// 	ERROR.Fatalln("Failed to listen:",
	// 		Server.ListenAndServeTLS(HttpSslCert, HttpSslKey))
	// } else {
	// 	listener, err := net.Listen(network, localAddress)
	// 	if err != nil {
	// 		ERROR.Fatalln("Failed to listen:", err)
	// 	}
	// 	ERROR.Fatalln("Failed to serve:", Server.Serve(listener))
	// }
}

func runStartupHooks() {
	for _, hook := range startupHooks {
		hook()
	}
}

func runShutdownHooks() {
	for _, hook := range shutdownHooks {
		hook()
	}
}

var startupHooks []func()
var shutdownHooks []func()

// Register a function to be run at app startup.
//
// The order you register the functions will be the order they are run.
// You can think of it as a FIFO queue.
// This process will happen after the config file is read
// and before the server is listening for connections.
//
// Ideally, your application should have only one call to init() in the file init.go.
// The reason being that the call order of multiple init() functions in
// the same package is undefined.
// Inside of init() call revel.OnAppStart() for each function you wish to register.
//
// Example:
//
//      // from: yourapp/app/controllers/somefile.go
//      func InitDB() {
//          // do DB connection stuff here
//      }
//
//      func FillCache() {
//          // fill a cache from DB
//          // this depends on InitDB having been run
//      }
//
//      // from: yourapp/app/init.go
//      func init() {
//          // set up filters...
//
//          // register startup functions
//          revel.OnAppStart(InitDB)
//          revel.OnAppStart(FillCache)
//      }
//
// This can be useful when you need to establish connections to databases or third-party services,
// setup app components, compile assets, or any thing you need to do between starting Revel and accepting connections.
//
func OnAppStart(f func()) {
	startupHooks = append(startupHooks, f)
}

// Register a function to be run at app shutdown.
//
// The order you register the functions will be the order they are run.
// You can think of it as a FIFO queue.
// This process will happen after the server has received a shutdown signal,
// and after the server has stopped listening for connections.
//
// If your application spawns it's own goroutines it will also be responsible
// for the graceful cleanup of those. Otherwise they will terminate with the main thread.
//
// Example:
//
//      // from: yourapp/app/controllers/somefile.go
//      func ShutdownBackgroundDBProcess() {
//          // waits for background process to complete,
//          // or sends a signal on a termination channel.
//      }
//
//      func CloseDB() {
//          // do DB cleanup stuff here
//      }
//
//      // from: yourapp/app/init.go
//      func init() {
//          // set up filters...
//
//          // register startup functions
//          revel.OnAppShutdown(ShutdownBackgroundDBProcess)
//          revel.OnAppShutdown(CloseDB)
//      }
//
// This can be useful when you need to close client connections, files,
// shutdown or terminate monitors and other goroutines.
//
func OnAppExit(f func()) {
	shutdownHooks = append(shutdownHooks, f)
}
