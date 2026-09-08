package uiverify

import (
	"context"
	"fmt"
	"io"
	"time"
)

// Result is what one clause assertion did.
type Result struct {
	ID            string
	Criterion     string
	Passed        bool
	Err           error
	Demonstrated  bool // the same check was shown going RED against a mutated surface
	DemoErr       error
	MutationBroke error // why the demonstration itself could not be made
	Duration      time.Duration
}

// RunWeb grades every rendered claim on the browser surface, and demonstrates that each check can
// fail. It returns the results and a nil error only when every check passed AND every check was
// demonstrated; a Refusal is returned unchanged so the caller can print it and exit non-zero.
func RunWeb(ctx context.Context, out io.Writer) ([]Result, error) {
	engines, err := FindEngines()
	if err != nil {
		return nil, err
	}
	sess, engine, err := NewSessionFrom(ctx, engines)
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	fmt.Fprintf(out, "engine:  %s (of %d found)\n", engine, len(engines))

	stack := NewStack()
	defer stack.Close()
	fmt.Fprintf(out, "stack:   %s (production /map and /static; scripted /v1/positions and /v1/stream)\n\n", stack.URL())

	runner := &Runner{S: sess, St: stack}
	checks := Checks()
	results := make([]Result, 0, len(checks))

	for _, c := range checks {
		res := Result{ID: c.ID, Criterion: c.Criterion}
		started := time.Now()

		// 1. The claim, measured on the pristine surface.
		stack.SetMutation(nil)
		stack.SetPositions(200, theFamily())
		stack.Restore()
		res.Err = c.Run(runner)
		res.Passed = res.Err == nil

		// 2. The demonstration: the SAME measuring code against a surface broken in one way. A
		//    check that stays green here is a check that cannot fail, which is not evidence.
		if c.Mutation == nil {
			res.MutationBroke = fmt.Errorf("%s has no mutation, so it can never be demonstrated", c.ID)
		} else {
			stack.SetMutation(c.Mutation)
			stack.SetPositions(200, theFamily())
			stack.Restore()
			res.DemoErr = c.Run(runner)
			res.Demonstrated = res.DemoErr != nil
			stack.SetMutation(nil)
		}
		res.Duration = time.Since(started)

		status := "PASS"
		if !res.Passed {
			status = "FAIL"
		}
		demo := "demonstrated"
		if !res.Demonstrated {
			demo = "NOT DEMONSTRATED"
		}
		fmt.Fprintf(out, "%-4s %-22s %-14s %6s  %s\n", status, res.ID, demo, res.Duration.Round(time.Millisecond), res.Criterion)
		if res.Err != nil {
			fmt.Fprintf(out, "       %v\n", res.Err)
		}
		if res.MutationBroke != nil {
			fmt.Fprintf(out, "       %v\n", res.MutationBroke)
		}
		if c.Mutation != nil && !res.Demonstrated {
			fmt.Fprintf(out, "       the mutation %q did not make this check fail; it cannot go red, so its pass is not evidence\n", c.Mutation.ID)
		}
		results = append(results, res)
	}
	return results, nil
}

// Summarise decides the exit verdict from the results. AC18 is enforced here: the demonstration
// count must equal the rendered-claim count, so no assertion can pass vacuously.
func Summarise(out io.Writer, surface string, results []Result) error {
	passed, demos := 0, 0
	for _, r := range results {
		if r.Passed {
			passed++
		}
		if r.Demonstrated {
			demos++
		}
	}
	fmt.Fprintf(out, "\n%s: %d/%d clause assertions passed, %d/%d demonstrated able to fail\n",
		surface, passed, len(results), demos, len(results))

	if passed != len(results) {
		return fmt.Errorf("%s: %d of %d clause assertions failed", surface, len(results)-passed, len(results))
	}
	if demos != len(results) {
		return fmt.Errorf("%s: %d rendered claims but only %d demonstrations (AC18): a check that was never shown going red is not evidence",
			surface, len(results), demos)
	}
	return nil
}
