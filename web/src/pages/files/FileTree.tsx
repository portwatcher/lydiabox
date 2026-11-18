import { ChevronRight, Folder, FolderOpen, House } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { files } from "../../api/files";
import { useServerDown } from "../../hooks/useSystem";
import type { RootName } from "../../api/types";
import { Spinner, cx } from "../../components/ui";
import { failedNode, loadedNode, loadingNode, showsLoadError } from "./folderNode";
import type { NodeState } from "./folderNode";

interface FileTreeProps {
  root: RootName;
  rootLabel: string;
  current: string;
  onOpen: (dir: string) => void;
  /** Bumped by the parent after changes so loaded folders refresh. */
  version: number;
}

/** Ancestors of `path`, from the root: "a/b/c" gives "", "a", "a/b", "a/b/c". */
function ancestors(path: string): string[] {
  const parts = path.split("/").filter(Boolean);
  return ["", ...parts.map((_, i) => parts.slice(0, i + 1).join("/"))];
}

/** Folder tree: a disclosure list that loads each folder's subfolders on expand. */
export function FileTree({ root, rootLabel, current, onOpen, version }: FileTreeProps) {
  const [nodes, setNodes] = useState<Record<string, NodeState>>({});
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set([""]));

  const load = useCallback(
    (dir: string) => {
      setNodes((all) => ({ ...all, [dir]: loadingNode(all[dir]) }));
      files
        .list(root, dir)
        .then((res) => {
          const children = res.entries.filter((e) => e.type === "dir").map((e) => e.path);
          setNodes((all) => ({ ...all, [dir]: loadedNode(children) }));
        })
        .catch((err: unknown) => setNodes((all) => ({ ...all, [dir]: failedNode(all[dir], err) })));
    },
    [root],
  );

  useEffect(() => {
    setNodes({});
    setExpanded(new Set([""]));
  }, [root]);

  // Keep the current folder's ancestors open and loaded.
  useEffect(() => {
    setExpanded((prev) => {
      const next = new Set(prev);
      ancestors(current).forEach((p) => next.add(p));
      return next;
    });
  }, [current, root]);

  // Reload every open folder after a change; expanding a folder loads it below.
  const expandedRef = useRef(expanded);
  const lastVersion = useRef(version);
  useEffect(() => {
    expandedRef.current = expanded;
  }, [expanded]);
  useEffect(() => {
    if (lastVersion.current === version) {
      return;
    }
    lastVersion.current = version;
    expandedRef.current.forEach((dir) => load(dir));
  }, [version, load]);

  const toggle = (dir: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(dir)) {
        next.delete(dir);
      } else {
        next.add(dir);
      }
      return next;
    });
  };

  // While the server cannot answer, nothing loads; at its end the open
  // folders that did not load do.
  const down = useServerDown();
  useEffect(() => {
    if (down) {
      return;
    }
    expanded.forEach((dir) => {
      if (!nodes[dir]) {
        load(dir);
      }
    });
  }, [expanded, nodes, load, down]);

  const renderNode = (dir: string) => {
    const node = nodes[dir];
    const open = expanded.has(dir);
    const name = dir === "" ? rootLabel : dir.slice(dir.lastIndexOf("/") + 1);
    const active = dir === current;
    const hasChildren = node?.children === null || node === undefined || (node.children?.length ?? 0) > 0;
    const Icon = dir === "" ? House : open ? FolderOpen : Folder;
    return (
      <li key={dir}>
        <div className={cx("flex items-center gap-0 p-0", active ? "menu-active" : "text-base-content/70 hover:text-base-content")}>
          <button
            type="button"
            aria-label={`${open ? "Collapse" : "Expand"} ${name}`}
            aria-expanded={open}
            onClick={() => toggle(dir)}
            className={cx("flex size-7 shrink-0 items-center justify-center rounded", !hasChildren && "invisible")}
          >
            {node?.loading && !node.children ? (
              <Spinner size="xs" className="" />
            ) : (
              <ChevronRight aria-hidden className={cx("size-3.5 transition-transform duration-150", open && "rotate-90")} />
            )}
          </button>
          <button
            type="button"
            onClick={() => onOpen(dir)}
            aria-current={active ? "page" : undefined}
            className="flex h-7 min-w-0 flex-1 items-center gap-1.5 pr-2 text-left text-[13px]"
            title={dir || rootLabel}
          >
            <Icon aria-hidden className="size-4 shrink-0" />
            <span className="truncate">{name}</span>
          </button>
        </div>
        {open && node?.children && node.children.length > 0 ? (
          <ul className="ms-3 ps-1">{node.children.map((child) => renderNode(child))}</ul>
        ) : null}
        {open && showsLoadError(node) ? (
          <ul className="ms-3 ps-1">
            <li className="menu-disabled">
              <span className="py-1 text-xs text-error">Folder not loaded</span>
            </li>
          </ul>
        ) : null}
      </li>
    );
  };

  return (
    <nav aria-label="Folders">
      <ul className="menu menu-sm w-full p-2">{renderNode("")}</ul>
    </nav>
  );
}
