package trash

import "github.com/Audarya07/dustpan/internal/env"

func envWithHome(h string) env.Env { return env.Env{Home: h} }
