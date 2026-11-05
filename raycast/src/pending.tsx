import { Action, ActionPanel, Icon, List, showToast, Toast } from "@raycast/api";
import { useCachedPromise } from "@raycast/utils";
import { useState } from "react";
import { Action as ElanAction, ago, dropName, elan, ownerNames } from "./elan";

export default function Command() {
  const [owner, setOwner] = useState("mine");
  const names = ownerNames();
  const { data, isLoading, revalidate } = useCachedPromise(
    (o: string) => elan<ElanAction[]>("actions", "pending", `--owner=${o}`, "--n=100"),
    [owner],
    { keepPreviousData: true },
  );

  async function update(a: ElanAction, status: "done" | "dropped") {
    const t = await showToast({ style: Toast.Style.Animated, title: status === "done" ? "Concluindo…" : "Cancelando…" });
    try {
      await elan("action", "update", String(a.id), `--status=${status}`);
      t.style = Toast.Style.Success;
      t.title = status === "done" ? "Concluída" : "Cancelada";
      revalidate();
    } catch (e) {
      t.style = Toast.Style.Failure;
      t.title = "Falhou";
      t.message = (e as Error).message;
    }
  }

  return (
    <List
      isLoading={isLoading}
      searchBarPlaceholder="Filtrar pendências…"
      searchBarAccessory={
        <List.Dropdown tooltip="De quem" value={owner} onChange={setOwner}>
          <List.Dropdown.Item title="Minhas" value="mine" />
          <List.Dropdown.Item title="Esperando de outros" value="others" />
        </List.Dropdown>
      }
    >
      {(data ?? []).map((a) => (
        <List.Item
          key={a.id}
          icon={Icon.Circle}
          title={owner === "mine" ? dropName(a.text, names) : a.text}
          subtitle={a.meeting_title}
          accessories={[{ text: ago(a.age_days) }]}
          actions={
            <ActionPanel>
              <Action title="Concluir" icon={Icon.CheckCircle} onAction={() => update(a, "done")} />
              <Action title="Cancelar" icon={Icon.XMarkCircle} shortcut={{ modifiers: ["cmd"], key: "d" }} onAction={() => update(a, "dropped")} />
              {a.meeting_id && <Action.OpenInBrowser title="Abrir reunião no Google Docs" url={`https://docs.google.com/document/d/${a.meeting_id}/edit`} />}
              {a.task_link && <Action.OpenInBrowser title="Abrir no Google Tasks" url={a.task_link} />}
              <Action.CopyToClipboard title="Copiar texto" content={a.text} />
            </ActionPanel>
          }
        />
      ))}
    </List>
  );
}
