package pkg

import (
	"fmt"
	"os"
	"os/exec"
)

// micromamba commands need to have rc files disabled and prefix specified
func (env *Environment) MicromambaInstallPackage(packageToInstall string, channel string) error {
	var installCmd *exec.Cmd
	if channel != "" {
		/*
			../../bin/micromamba install --no-rc -c conda-forge -y --prefix /Users/richardinsley/Projects/comfycli/jumpboot/tests/mlx/micromamba/envs/myenv3.10 mlx
		*/
		installCmd = exec.Command(env.MicromambaPath, "install", "--no-rc", "-c", channel, "--prefix", env.EnvPath, "-y", packageToInstall)
	} else {
		installCmd = exec.Command(env.MicromambaPath, "install", "--no-rc", "--prefix", env.EnvPath, "-y", packageToInstall)
	}

	installCmd.Stdout = os.Stdout
	installCmd.Stderr = os.Stderr
	if err := installCmd.Run(); err != nil {
		return fmt.Errorf("error installing package: %v", err)
	}
	return nil
}
