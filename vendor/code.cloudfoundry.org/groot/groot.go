package groot

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"

	"code.cloudfoundry.org/groot/fetcher"
	"code.cloudfoundry.org/groot/fetcher/filefetcher"
	"code.cloudfoundry.org/groot/fetcher/layerfetcher"
	"code.cloudfoundry.org/groot/fetcher/layerfetcher/source"
	"code.cloudfoundry.org/groot/imagepuller"
	"code.cloudfoundry.org/lager"
	"github.com/containers/image/types"
	runspec "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/urfave/cli"
)

type DiskUsage struct {
	TotalBytesUsed     int64 `json:"total_bytes_used"`
	ExclusiveBytesUsed int64 `json:"exclusive_bytes_used"`
}

type VolumeStats struct {
	DiskUsage DiskUsage `json:"disk_usage"`
}

// Driver should implement the filesystem interaction
//go:generate counterfeiter . Driver
type Driver interface {
	Unpack(logger lager.Logger, layerID string, parentIDs []string, layerTar io.Reader) error
	Bundle(logger lager.Logger, bundleID string, layerIDs []string, spec BundleSpec) (runspec.Spec, error)
	Exists(logger lager.Logger, layerID string) bool
	Delete(logger lager.Logger, bundleID string) error
	Stats(logger lager.Logger, bundleID string) (VolumeStats, error)
}

// ImagePuller should be able to download and store a remote (or local) image
// and return all its layer information so that it can be bundled together by
// the driver
//go:generate counterfeiter . ImagePuller
type ImagePuller interface {
	Pull(logger lager.Logger, spec imagepuller.ImageSpec) (imagepuller.Image, error)
}

type Groot struct {
	Driver      Driver
	Logger      lager.Logger
	ImagePuller ImagePuller
}

func Run(driver Driver, argv []string, driverFlags []cli.Flag) {
	// The `Before` closure sets this. This is ugly, but we don't know the log
	// level until the CLI framework has parsed the flags.
	var g *Groot

	app := cli.NewApp()
	app.Usage = "A garden image plugin"
	app.Flags = append([]cli.Flag{
		cli.StringFlag{
			Name:  "config",
			Value: "",
			Usage: "Path to config file",
		},
	}, driverFlags...)
	app.Commands = []cli.Command{
		{
			Name: "create",
			Flags: []cli.Flag{
				cli.Int64Flag{
					Name:  "disk-limit-size-bytes",
					Usage: "Inclusive disk limit (i.e: includes all layers in the filesystem)",
				},
				cli.BoolFlag{
					Name:  "exclude-image-from-quota",
					Usage: "Set disk limit to be exclusive (i.e.: excluding image layers)",
				},
			},
			Action: func(ctx *cli.Context) error {
				rootfsURI, err := url.Parse(ctx.Args()[0])
				if err != nil {
					return err
				}

				handle := ctx.Args()[1]
				runtimeSpec, err := g.Create(handle, rootfsURI, ctx.Int64("disk-limit-size-bytes"), ctx.Bool("exclude-image-from-quota"))
				if err != nil {
					return err
				}

				return json.NewEncoder(os.Stdout).Encode(runtimeSpec)
			},
		},
		{
			Name: "pull",
			Action: func(ctx *cli.Context) error {
				rootfsURI, err := url.Parse(ctx.Args()[0])
				if err != nil {
					return err
				}

				return g.Pull(rootfsURI)
			},
		},
		{
			Name: "delete",
			Action: func(ctx *cli.Context) error {
				handle := ctx.Args()[0]
				return g.Delete(handle)
			},
		},
		{
			Name: "stats",
			Action: func(ctx *cli.Context) error {
				handle := ctx.Args()[0]
				stats, err := g.Stats(handle)
				if err != nil {
					return err
				}
				return json.NewEncoder(os.Stdout).Encode(stats)
			},
		},
	}
	app.Before = func(ctx *cli.Context) error {
		conf, err := parseConfig(ctx.GlobalString("config"))
		if err != nil {
			return silentError(err)
		}
		g, err = newGroot(driver, conf)
		if err != nil {
			return silentError(err)
		}
		return nil
	}

	if err := app.Run(argv); err != nil {
		if _, ok := err.(SilentError); !ok {
			fmt.Println(err)
		}
		os.Exit(1)
	}
}

func newGroot(driver Driver, conf config) (*Groot, error) {
	logger, err := newLogger(conf.LogLevel)
	if err != nil {
		return nil, err
	}

	fileFetcher := filefetcher.NewFileFetcher()
	source := source.NewLayerSource(types.SystemContext{}, false)
	layerFetcher := layerfetcher.NewLayerFetcher(&source)
	fetcher := fetcher.Fetcher{
		FileFetcher:  fileFetcher,
		LayerFetcher: layerFetcher,
	}

	imagePuller := imagepuller.NewImagePuller(&fetcher, driver)

	return &Groot{
		Driver:      driver,
		Logger:      logger,
		ImagePuller: imagePuller,
	}, nil
}

func newLogger(logLevelStr string) (lager.Logger, error) {
	logLevels := map[string]lager.LogLevel{
		"debug": lager.DEBUG,
		"info":  lager.INFO,
		"error": lager.ERROR,
		"fatal": lager.FATAL,
	}

	logLevel, ok := logLevels[logLevelStr]
	if !ok {
		return nil, fmt.Errorf("invalid log level: %s", logLevelStr)
	}

	logger := lager.NewLogger("groot")
	logger.RegisterSink(lager.NewWriterSink(os.Stderr, logLevel))

	return logger, nil
}

// SilentError silences errors. urfave/cli already prints certain errors, we
// don't want to print them twice
type SilentError struct {
	Underlying error
}

func (e SilentError) Error() string {
	return e.Underlying.Error()
}

func silentError(err error) SilentError {
	return SilentError{Underlying: err}
}
