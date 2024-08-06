package pkg

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"

	"github.com/schollz/progressbar/v3"
)

func (env *Environment) PipInstallPackages(packages []string, index_url string, extra_index_url string, no_cache bool, feedback CreateEnvironmentOptions) error {
	args := []string{
		"install",
		"--no-warn-script-location",
	}

	if no_cache {
		args = append(args, "--no-cache-dir")
	}

	args = append(args, packages...)
	if index_url != "" {
		args = append(args, "--index-url", index_url)
	}
	if extra_index_url != "" {
		args = append(args, "--extra-index-url", extra_index_url)
	}

	installCmd := exec.Command(env.PipPath, args...)
	if feedback == ShowVerbose || feedback == ShowNothing {
		if feedback == ShowVerbose {
			fmt.Printf("Installing pip packages: %v\n", packages)
			installCmd.Stdout = os.Stdout
			installCmd.Stderr = os.Stderr
		} else {
			installCmd.Stdout = nil
			installCmd.Stderr = nil
		}
		if err := installCmd.Run(); err != nil {
			return fmt.Errorf("error installing package: %v", err)
		}
	} else {
		var bar *progressbar.ProgressBar = nil

		bardesc := "Installing pip packages..."
		if len(packages) == 1 {
			bardesc = fmt.Sprintf("Installing pip package %s...", packages[0])
		}
		if feedback == ShowProgressBar || feedback == ShowProgressBarVerbose {
			bar = progressbar.NewOptions(-1,
				progressbar.OptionEnableColorCodes(true),
				progressbar.OptionShowBytes(false),
				progressbar.OptionSetWidth(15),
				progressbar.OptionSetDescription(bardesc),
				progressbar.OptionSetTheme(progressbar.Theme{
					Saucer:        "[green]=[reset]",
					SaucerHead:    "[green]>[reset]",
					SaucerPadding: " ",
					BarStart:      "[",
					BarEnd:        "]",
				}))
		}

		if feedback == ShowProgressBarVerbose {
			fmt.Printf("Installing pip packages: %v\n", packages)
		}

		stdout, err := installCmd.StdoutPipe()
		if err != nil {
			return nil
		}
		defer stdout.Close()

		// continue to read the output until there is no more
		// or an error occurs
		if err := installCmd.Start(); err != nil {
			return nil
		}

		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if bar != nil {
				// we'll use lines to update the progress bar to show we are working
				bar.Add(1)
			}
			if feedback == ShowVerbose || feedback == ShowProgressBarVerbose {
				fmt.Println(scanner.Text())
			}
		}

		if bar != nil {
			bar.Finish()
			fmt.Println()
		}
	}
	return nil
}

func (env *Environment) PipInstallRequirmements(requirementsPath string, feedback CreateEnvironmentOptions) error {
	installCmd := exec.Command(env.PipPath, "install", "--no-warn-script-location", "-r", requirementsPath)

	if feedback == ShowVerbose || feedback == ShowNothing {
		installCmd.Stdout = os.Stdout
		installCmd.Stderr = os.Stderr
		if err := installCmd.Run(); err != nil {
			return fmt.Errorf("error installing requirements: %v", err)
		}
	} else {
		var bar *progressbar.ProgressBar = nil

		if feedback == ShowProgressBar || feedback == ShowProgressBarVerbose {
			bar = progressbar.NewOptions(-1,
				progressbar.OptionEnableColorCodes(true),
				progressbar.OptionShowBytes(false),
				progressbar.OptionSetWidth(15),
				progressbar.OptionSetDescription("Installing pip requirements..."),
				progressbar.OptionSetTheme(progressbar.Theme{
					Saucer:        "[green]=[reset]",
					SaucerHead:    "[green]>[reset]",
					SaucerPadding: " ",
					BarStart:      "[",
					BarEnd:        "]",
				}))
		}

		stdout, err := installCmd.StdoutPipe()
		if err != nil {
			return nil
		}
		defer stdout.Close()

		// continue to read the output until there is no more
		// or an error occurs
		if err := installCmd.Start(); err != nil {
			return nil
		}

		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if bar != nil {
				// we'll use lines to update the progress bar to show we are working
				bar.Add(1)
			}
			if feedback == ShowVerbose || feedback == ShowProgressBarVerbose {
				fmt.Println(scanner.Text())
			}
		}

		if bar != nil {
			bar.Finish()
			fmt.Println()
		}
	}
	return nil
}

func (env *Environment) PipInstallPackage(packageToInstall string, index_url string, extra_index_url string, no_cache bool, feedback CreateEnvironmentOptions) error {
	packages := []string{
		packageToInstall,
	}
	return env.PipInstallPackages(packages, index_url, extra_index_url, no_cache, feedback)
}
