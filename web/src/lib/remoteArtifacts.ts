import type { ChangeDesc } from "@codemirror/state";

import type { Operation } from "./types";
import { runeLength, sliceByCodePoint, toCodeUnitOffset } from "./ot";

export type RemoteArtifactKind = "insert" | "replace" | "delete";

export interface RemoteInsertArtifact {
  id: string;
  groupId: string;
  kind: "insert" | "replace";
  author: string;
  from: number;
  to: number;
}

export interface RemoteDeleteArtifact {
  id: string;
  groupId: string;
  kind: "delete";
  author: string;
  position: number;
  text: string;
}

export type RemoteArtifact = RemoteInsertArtifact | RemoteDeleteArtifact;

let nextRemoteArtifactID = 0;
let nextRemoteArtifactGroupID = 0;

function createRemoteArtifactID() {
  nextRemoteArtifactID += 1;
  return `remote-artifact-${nextRemoteArtifactID}`;
}

function createRemoteArtifactGroupID() {
  nextRemoteArtifactGroupID += 1;
  return `remote-artifact-group-${nextRemoteArtifactGroupID}`;
}

export function resetRemoteArtifactIDs() {
  nextRemoteArtifactID = 0;
  nextRemoteArtifactGroupID = 0;
}

export function getRemoteArtifactTitle(artifact: Pick<RemoteArtifact, "author" | "kind">) {
  if (artifact.kind === "delete") {
    return `Deleted by ${artifact.author}`;
  }
  return `Edited by ${artifact.author}`;
}

export function buildRemoteArtifactsForOperation(previousSource: string, nextSource: string, op: Operation): RemoteArtifact[] {
  const author = op.author?.trim() || "Someone else";
  const deletedText = op.delete_count > 0 ? sliceByCodePoint(previousSource, op.position, op.position + op.delete_count) : "";
  const groupId = createRemoteArtifactGroupID();
  const artifacts: RemoteArtifact[] = [];

  if (deletedText && op.insert_text) {
    const insertedLength = runeLength(op.insert_text);
    const from = toCodeUnitOffset(nextSource, op.position);
    const to = toCodeUnitOffset(nextSource, op.position + insertedLength);
    if (from < to) {
      artifacts.push({
        id: createRemoteArtifactID(),
        groupId,
        kind: "replace",
        author,
        from,
        to,
      });
    }
    artifacts.push({
      id: createRemoteArtifactID(),
      groupId,
      kind: "delete",
      author,
      position: toCodeUnitOffset(nextSource, op.position + insertedLength),
      text: deletedText,
    });
    return artifacts;
  }

  const insertedLength = runeLength(op.insert_text);
  if (insertedLength > 0) {
    const from = toCodeUnitOffset(nextSource, op.position);
    const to = toCodeUnitOffset(nextSource, op.position + insertedLength);
    if (from < to) {
      artifacts.push({
        id: createRemoteArtifactID(),
        groupId,
        kind: "insert",
        author,
        from,
        to,
      });
    }
  }

  if (deletedText) {
    artifacts.push({
      id: createRemoteArtifactID(),
      groupId,
      kind: "delete",
      author,
      position: toCodeUnitOffset(nextSource, op.position),
      text: deletedText,
    });
  }

  return artifacts;
}

export function mapRemoteArtifacts(artifacts: RemoteArtifact[], changes: ChangeDesc) {
  return artifacts.flatMap((artifact) => {
    if (artifact.kind === "delete") {
      return {
        ...artifact,
        position: changes.mapPos(artifact.position, -1),
      };
    }

    const from = changes.mapPos(artifact.from, 1);
    const to = changes.mapPos(artifact.to, -1);
    if (from >= to) {
      return [];
    }
    return {
      ...artifact,
      from,
      to,
    };
  });
}
