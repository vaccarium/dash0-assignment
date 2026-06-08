package main

import (
	"fmt"
	"os"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
)

type config struct {
	ListenAddr            string           `toml:"listen_addr"`
	MaxReceiveMessageSize int              `toml:"max_receive_message_size"`
	EnableReflection      *bool            `toml:"enable_reflection"`
	DiagnosticInterval    string           `toml:"diagnostic_interval"`
	ClickHouse            clickhouseConfig `toml:"clickhouse"`
}

type clickhouseConfig struct {
	Addr     string `toml:"addr"`
	Database string `toml:"database"`
	Username string `toml:"username"`
	Password string `toml:"password"`
}

func loadConfig(path string) (*config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	var cfg config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}
	return &cfg, nil
}

// applyConfig sets flag variables from config for any flag that was not
// explicitly provided on the command line.
func applyConfig(cmd *cobra.Command, cfg *config) {
	if !cmd.Flags().Changed("listenAddr") && cfg.ListenAddr != "" {
		listenAddr = cfg.ListenAddr
	}
	if !cmd.Flags().Changed("maxReceiveMessageSize") && cfg.MaxReceiveMessageSize != 0 {
		maxReceiveMessageSize = cfg.MaxReceiveMessageSize
	}
	if !cmd.Flags().Changed("enableReflection") && cfg.EnableReflection != nil {
		enableReflection = *cfg.EnableReflection
	}
	if !cmd.Flags().Changed("diagnosticInterval") && cfg.DiagnosticInterval != "" {
		if d, err := time.ParseDuration(cfg.DiagnosticInterval); err == nil {
			diagnosticInterval = d
		}
	}
	if !cmd.Flags().Changed("clickhouseAddr") && cfg.ClickHouse.Addr != "" {
		clickhouseAddr = cfg.ClickHouse.Addr
	}
	if !cmd.Flags().Changed("clickhouseDatabase") && cfg.ClickHouse.Database != "" {
		clickhouseDatabase = cfg.ClickHouse.Database
	}
	if !cmd.Flags().Changed("clickhouseUsername") && cfg.ClickHouse.Username != "" {
		clickhouseUsername = cfg.ClickHouse.Username
	}
	if !cmd.Flags().Changed("clickhousePassword") && cfg.ClickHouse.Password != "" {
		clickhousePassword = cfg.ClickHouse.Password
	}
}
