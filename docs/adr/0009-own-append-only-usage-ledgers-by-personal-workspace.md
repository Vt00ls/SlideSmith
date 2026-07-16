# Own append-only usage ledgers by personal workspace

Each Personal Workspace owns an append-only Usage Ledger, while every usage entry retains attribution to the Task, Phase Run, Runtime Run, provider, model, and measured resource dimensions that produced it. Failed execution remains chargeable when consumption occurred, corrections use offsetting entries rather than rewriting history, and later Task transfer does not move prior usage to a different owner. This preserves per-User quota and audit integrity without duplicating ownership across execution records.
