import { Action, ActionPanel, Detail, Icon, List } from "@raycast/api";
import { useCachedPromise } from "@raycast/utils";
import { useState } from "react";
import { elan, elanText, hm, Note, SearchHit, shortDate } from "./elan";

function Meeting({ id, title }: { id: string; title: string }) {
  const { data, isLoading } = useCachedPromise((m: string) => elanText("meeting", m, "--markdown"), [id]);
  return (
    <Detail
      isLoading={isLoading}
      navigationTitle={title}
      markdown={data ?? ""}
      actions={
        <ActionPanel>
          <Action.OpenInBrowser title="Abrir no Google Docs" url={`https://docs.google.com/document/d/${id}/edit`} />
          <Action.CopyToClipboard title="Copiar Markdown" content={data ?? ""} />
        </ActionPanel>
      }
    />
  );
}

const kindLabel: Record<string, string> = { summary: "resumo", decision: "decisão", action: "ação", fact: "fato" };

export default function Command() {
  const [query, setQuery] = useState("");
  const { data, isLoading, error } = useCachedPromise(
    (q: string) => (q.trim().length < 2 ? Promise.resolve([] as SearchHit[]) : elan<SearchHit[]>("search", q, "--n=20")),
    [query],
    { keepPreviousData: true },
  );
  const idle = query.trim().length < 2;
  const notes = useCachedPromise(() => elan<Note[]>("notes", "--n=30"), [], { execute: idle, keepPreviousData: true });
  const showNotes = idle && (notes.data ?? []).length > 0;
  return (
    <List
      isLoading={isLoading || (idle && notes.isLoading)}
      onSearchTextChange={setQuery}
      throttle
      searchBarPlaceholder="Buscar nas reuniões e notas…"
      isShowingDetail={(data ?? []).length > 0 || showNotes}
    >
      {error && <List.EmptyView icon={Icon.Warning} title="Erro na busca" description={error.message} />}
      {showNotes && (
        <List.Section title="Notas recentes">
          {notes.data!.map((n) => (
            <List.Item
              key={`note-${n.id}`}
              icon={Icon.Pencil}
              title={n.text.split("\n")[0]}
              accessories={[{ text: `${shortDate(n.created_at)} ${hm(n.created_at)}` }]}
              detail={<List.Item.Detail markdown={`${n.text}\n\n_${shortDate(n.created_at)} ${hm(n.created_at)} · ${n.source}_`} />}
              actions={
                <ActionPanel>
                  <Action.CopyToClipboard title="Copiar nota" content={n.text} />
                  <Action.Paste title="Colar no app da frente" content={n.text} />
                </ActionPanel>
              }
            />
          ))}
        </List.Section>
      )}
      {(data ?? []).map((h, i) => (
        <List.Item
          key={`${h.meeting_id}-${i}`}
          icon={h.type === "note" ? Icon.Pencil : Icon.Document}
          title={h.title || "nota"}
          accessories={[{ text: shortDate(h.date) }]}
          detail={<List.Item.Detail markdown={`**${h.title || "nota"}** · ${shortDate(h.date)} · ${kindLabel[h.kind] ?? h.type}\n\n${h.text}`} />}
          actions={
            <ActionPanel>
              {h.meeting_id && <Action.Push title="Abrir reunião" icon={Icon.Document} target={<Meeting id={h.meeting_id} title={h.title} />} />}
              {h.meeting_id && <Action.OpenInBrowser title="Abrir no Google Docs" url={`https://docs.google.com/document/d/${h.meeting_id}/edit`} />}
              <Action.CopyToClipboard title="Copiar texto" content={h.text} />
            </ActionPanel>
          }
        />
      ))}
    </List>
  );
}
