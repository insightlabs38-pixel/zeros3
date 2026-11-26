# C18 failure domains

| Boundary | Evidence or observed behavior | Operation after fault |
| --- | --- | --- |
| Acknowledged namespace update | The branch's FB-01R watermark and journal tests distinguish a durable acknowledged prefix from a later torn tail. The C18 suite repeats the actual process cuts. | Use the verified replay or explicit inspected salvage path. Preserve a rejected store for inspection. |
| Damaged checkpoint | The foundational recovery checks the checkpoint trailer's journal binding and verifies the valid journal prefix before rebuild. | Rebuild from a proven journal. Ambiguous roots need operator recovery. |
| Pack staging and rename | C13 and C17 cut a child before and after publication. Durable references survive restart. A partial pack can remain staging only. | Reopen, verify and retry. Reclaim staging after exclusive maintenance. |
| One preset dictionary copy | The C18 copy check detects loss of redundancy while exact CAS reads can use the surviving copy. | Repair from the twin under exclusive access and prove the original pack identity before rename. |
| Both copies or unrelated pack bytes | The candidate repair fails closed. Pack-local redundancy shares the same physical file. | A verified backup, peer or other independent CAS copy is necessary. |
| Canonical/deep dependency | Closure proofs detect missing base or component and refuse an incomplete published target. The peer-recovery test uses exact affected digests. | Recover the authoritative closure from the named peer, or restore a portable verified bundle. |
| Derived similarity generation | C15 scoped hints resolve against current live manifests. Stale strict views and corrupt runs are refused. Actual process cuts preserve a complete current pointer or a recoverable old one. | Rebuild or fall back to the ordinary candidate search. Objects remain authoritative separately. |
| Thin transfer and journal visibility | C16 and C17 kill after staging, component and manifest publication and journal sync. Retry preserves the acknowledged version and proves every byte. | Replay the exact request. Reject unsupported or mismatched named dependencies. |
| Portable restore and old reader | C13 bundles preserved exact logical bytes with the pack feature present. C18 repeats ordinary snapshot and bundle consumption across platforms. | Transfer the portable logical snapshot. Keep codec-specific files inside a compatible physical store. |

A process cut leaves the host filesystem and page cache running. Device
ordering, power-loss durability, correlated media failure and large-scale
operator recovery need separate qualification.
