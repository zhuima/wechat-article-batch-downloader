//go:build windows

package cmd

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"mp_article_batch_downloader/pkg/certificate"
	"mp_article_batch_downloader/pkg/system"
)

func init() {
	root_cmd.AddCommand(&cobra.Command{
		Use:   "desktop-prepare",
		Short: "将本机连接证书加入当前用户的信任列表",
		RunE: func(cmd *cobra.Command, args []string) error {
			installed, err := certificate.CheckInstalled(CertFiles.Cert)
			if err != nil {
				return err
			}
			if installed {
				return nil
			}
			if err := certificate.InstallCertificate(CertFiles.Cert); err != nil {
				return fmt.Errorf("信任本机连接证书失败: %w", err)
			}
			installed, err = certificate.CheckInstalled(CertFiles.Cert)
			if err != nil {
				return err
			}
			if !installed {
				return fmt.Errorf("证书安装后未出现在当前用户的根证书列表")
			}
			return nil
		},
	})
	root_cmd.AddCommand(&cobra.Command{
		Use:   "desktop-recover",
		Short: "恢复启动前的 Windows 系统代理设置",
		RunE: func(cmd *cobra.Command, args []string) error {
			return system.DisableProxy(system.ProxySettings{})
		},
	})
	root_cmd.AddCommand(&cobra.Command{
		Use:   "desktop-ensure-proxy",
		Short: "重新接入本次 Windows 客户端拥有的系统代理",
		RunE: func(cmd *cobra.Command, args []string) error {
			return system.EnsureDesktopProxy(system.ProxySettings{
				Hostname: viper.GetString("proxy.hostname"),
				Port:     strconv.Itoa(viper.GetInt("proxy.port")),
			})
		},
	})
}

func watchDesktopParent(ctx context.Context, stop context.CancelFunc) {
	pid, err := strconv.Atoi(os.Getenv("MP_ARCHIVE_PARENT"))
	if err != nil || pid <= 1 {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !system.IsProcessRunning(pid) {
					stop()
					return
				}
			}
		}
	}()
}
