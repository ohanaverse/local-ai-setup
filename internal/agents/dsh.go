package agents

// dshLaunch builds the shared `ollama launch dsh` command for the three dsh
// run modes. profile is the dsh `--profile` selector ("" for the default
// browser mode); model is the bare model name passed via `--model` ("" for the
// command modes that reuse dsh's stored settings); args are the user's
// passthrough args, forwarded after the `--` separator so ollama hands them to
// dsh instead of parsing them as launcher flags.
//
// `ollama launch` itself defines only --model/--config/--restore/--yes; every
// other flag (--profile, --port, the headless task string) is a dsh flag and
// must follow the `--` separator to reach dsh.
func dshLaunch(profile, model string, args []string) LaunchCmd {
	lc := LaunchCmd{Bin: "ollama"}
	lc.Args = append(lc.Args, "launch", "dsh")
	if model != "" {
		lc.Args = append(lc.Args, "--model", model)
	}
	if profile != "" {
		// --profile is a dsh flag, not an ollama launch flag, so it must
		// follow the `--` separator.
		lc.Args = append(lc.Args, "--", "--profile", profile)
	}
	if len(args) > 0 {
		if profile == "" {
			// No profile yet: the passthrough args need their own `--`.
			lc.Args = append(lc.Args, "--")
		}
		lc.Args = append(lc.Args, args...)
	}
	return lc
}
