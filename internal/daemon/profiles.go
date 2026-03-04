package daemon

import (
	"fmt"
	"strings"
)

var taskProfiles = map[string]string{
	"architect": `Operate as a software architect.
- Prioritise high-level design, interfaces, data flow, and migration strategy.
- Call out trade-offs explicitly before implementation.
- Keep implementation changes minimal unless requested.
`,
	"developer": `Operate as a software developer.
- Implement requested changes end-to-end with pragmatic scope.
- Keep diffs focused, maintainable, and aligned with existing code style.
- Add or update tests when behavior changes.
`,
	"qa": `Operate as a QA reviewer.
- Prioritise finding defects, regressions, and missing tests.
- Validate edge cases and failure paths, not only happy paths.
- Report concrete findings with severity and reproducible evidence.
`,
	"refactorer": `Operate as a refactoring specialist.
- Improve structure and readability without changing intended behavior.
- Prefer small safe transformations and preserve external interfaces.
- Note any behavior-risky spots before touching them.
`,
	"incident": `Operate as an incident responder.
- Optimise for fastest safe mitigation and clear root-cause analysis.
- Minimise blast radius and propose rollback/fallback where relevant.
- Include concrete verification steps proving mitigation.
`,
	"ux-reviewer": `Operate as a UX reviewer from an end-user perspective.
- Evaluate user flows, friction points, empty/error/loading states, and clarity of copy.
- Prioritise findings by user impact and frequency.
- Propose concrete UI/UX fixes that are directly actionable for implementation.
`,
}

func applyTaskProfile(prompt, profile string) string {
	p := strings.TrimSpace(strings.ToLower(profile))
	if p == "" {
		return prompt
	}
	overlay, ok := taskProfiles[p]
	if !ok {
		return prompt
	}
	return fmt.Sprintf("Execution profile: %s\n\n%s\nTask:\n%s", p, overlay, prompt)
}
