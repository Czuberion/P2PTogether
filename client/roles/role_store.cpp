#include "role_store.h"
#include <QDebug>
#include <QReadLocker>
#include <QWriteLocker>

namespace P2P {
namespace Roles {

RoleStore::RoleStore(QObject* parent) : QObject(parent) {
    qRegisterMetaType<client::p2p::RoleDefinitionsUpdate>(
        "client::p2p::RoleDefinitionsUpdate");
    qRegisterMetaType<client::p2p::PeerRoleAssignment>(
        "client::p2p::PeerRoleAssignment");
    qRegisterMetaType<client::p2p::AllPeerRoleAssignments>(
        "client::p2p::AllPeerRoleAssignments");
    qRegisterMetaType<client::p2p::LocalPeerIdentity>(
        "client::LocalPeerIdentity");
    // qRegisterMetaType<client::ServerMsg>("client::ServerMsg"); // Already
    // done by ControlStreamWorker probably
}

RoleStore::~RoleStore() {}

void RoleStore::processRoleDefinitionsUpdate(
    const client::p2p::RoleDefinitionsUpdate& update) {
    int newSize = 0;
    {
        QWriteLocker locker(&storeLock);
        roleDefinitions.clear();
        for (const auto& defProto : update.definitions()) {
            if (defProto.name().empty()) {
                qWarning() << "RoleStore: Received role definition with empty "
                              "name. Skipping.";
                continue;
            }
            // keep original spelling for display, but use a lower‑case key for
            // look‑ups
            const QString key =
                QString::fromStdString(defProto.name()).toLower();
            roleDefinitions.insert(key, defProto);
        }
        newSize = roleDefinitions.size();
    }
    // No direct data from the update is passed in signals, so emitting after
    // lock is fine. The boolean 'changed' isn't strictly needed here as we
    // always clear and repopulate. We assume if this method is called, it's a
    // valid update warranting signals.
    qInfo() << "RoleStore: Processed RoleDefinitionsUpdate. Total definitions:"
            << newSize;
    qDebug()
        << "RoleStore: Emitting definitionsChanged and allAssignmentsRefreshed";
    emit definitionsChanged();
    emit allAssignmentsRefreshed(); // Definitions changing can affect all
                                    // permissions
}

void RoleStore::processPeerRoleAssignment(
    const client::p2p::PeerRoleAssignment& assignment) {
    QString peerId = QString::fromStdString(assignment.peer_id());
    if (peerId.isEmpty()) {
        qWarning() << "RoleStore: Received PeerRoleAssignment with empty "
                      "peer_id. Skipping.";
        return;
    }

    bool changed = false;
    {
        QWriteLocker locker(&storeLock);

        auto it = peerAssignments.find(peerId);
        if (it == peerAssignments.end() ||
            assignment.hlc_ts() > it.value().hlc_ts()) {
            if (assignment.assigned_role_names_size() > 0) {
                peerAssignments.insert(peerId, assignment);
            } else {
                if (it != peerAssignments.end()) {
                    peerAssignments.erase(it);
                }
            }
            changed = true;
            qInfo() << "RoleStore: Processed PeerRoleAssignment for peer"
                    << peerId << "New HLC:" << assignment.hlc_ts();
        } else {
            qInfo() << "RoleStore: Skipped stale PeerRoleAssignment for peer"
                    << peerId << "Incoming HLC:" << assignment.hlc_ts()
                    << "Existing HLC:"
                    << (it != peerAssignments.end() ? it.value().hlc_ts() : -1);
        }
    }
    // locker goes out of scope here

    if (changed) {
        qDebug() << "RoleStore: Emitting peerRolesChanged for" << peerId;
        emit peerRolesChanged(peerId);
    }
}

void RoleStore::processAllPeerAssignments(
    const client::p2p::AllPeerRoleAssignments& allAssignments) {
    // This is a full snapshot. Be careful about HLC.
    // Option 1: Clear all and replace, trusting the snapshot's HLC.
    // Option 2: Iterate and call processPeerRoleAssignment for each, respecting
    // individual HLCs. Option 2 is generally safer against overwriting newer
    // individual updates with an older snapshot.

    // TODO: Consider HLC for the snapshot message itself
    // (allAssignments.hlc_ts())
    //       to decide if this whole snapshot should even be processed if it's
    //       older than the last time a full snapshot was applied. For now,
    //       process individual items.

    qInfo() << "RoleStore: Processing AllPeerAssignments snapshot. HLC:"
            << allAssignments.hlc_ts()
            << "Items:" << allAssignments.assignments_size();

    // To avoid emitting peerRolesChanged for every single peer if many change,
    // we can lock, do all updates, then emit one "allAssignmentsRefreshed"
    // signal. However, processPeerRoleAssignment already emits
    // peerRolesChanged. For simplicity now, let's just call
    // processPeerRoleAssignment for each. A more optimized version might batch
    // UI updates.

    bool anyChange = false;
    for (const auto& assignmentProto : allAssignments.assignments()) {
        QString peerId = QString::fromStdString(assignmentProto.peer_id());
        if (peerId.isEmpty())
            continue;

        // Temporarily unlock for individual processing, then re-lock if needed,
        // or handle locking inside. For now, let processPeerRoleAssignment
        // handle its own locking. This means multiple lock/unlock cycles.
        processPeerRoleAssignment(
            assignmentProto); // This will emit individual signals
        // To track if *any* change happened for the allAssignmentsRefreshed
        // signal: We'd need processPeerRoleAssignment to return a bool.
    }
    qInfo() << "RoleStore: Finished processing AllPeerAssignments snapshot.";
    // After all individual assignments are processed:
    qDebug() << "RoleStore: Emitting allAssignmentsRefreshed";
    emit
    allAssignmentsRefreshed(); // Signal that a bulk update may have occurred
}

QMap<QString, client::p2p::RoleDefinition>
RoleStore::getRoleDefinitions() const {
    QReadLocker locker(&storeLock);
    return roleDefinitions; // Returns a copy
}

client::p2p::RoleDefinition
RoleStore::getRoleDefinition(const QString& roleName, bool* ok) const {
    QReadLocker locker(&storeLock);
    // Consider normalizing roleName (e.g., toLower()) if keys are stored
    // normalized
    const QString key = roleName.toLower();
    if (roleDefinitions.contains(key)) {
        if (ok)
            *ok = true;
        return roleDefinitions.value(key);
    }
    if (ok)
        *ok = false;
    return client::p2p::RoleDefinition(); // Return default/empty if not found
}

QVector<QString> RoleStore::getAssignedRoleNames(const QString& peerId) const {
    QReadLocker locker(&storeLock);
    auto it = peerAssignments.find(peerId);
    if (it != peerAssignments.end()) {
        const auto& assignmentProto = it.value();
        QVector<QString> names;
        names.reserve(assignmentProto.assigned_role_names_size());
        for (const std::string& roleNameStd :
             assignmentProto.assigned_role_names()) {
            names.append(QString::fromStdString(roleNameStd));
        }
        return names;
    }
    return QVector<QString>();
}

qint64 RoleStore::getPeerAssignmentHlc(const QString& peerId) const {
    QReadLocker locker(&storeLock);
    auto it = peerAssignments.find(peerId);
    if (it != peerAssignments.end()) {
        return it.value().hlc_ts();
    }
    return -1; // Or some other indicator of not found/no HLC
}

quint32 RoleStore::getPermissionsForPeer(const QString& peerId) const {
    quint32 finalPermissions = 0;
    QReadLocker locker(&storeLock); // Lock for the duration of calculation

    auto it = peerAssignments.find(peerId);
    if (it == peerAssignments.end()) {
        return 0; // No roles assigned, so no permissions
    }

    const auto& assignmentProto = it.value();
    for (const std::string& roleNameStd :
         assignmentProto.assigned_role_names()) {
        const QString key = QString::fromStdString(roleNameStd).toLower();
        if (roleDefinitions.contains(key)) {
            finalPermissions |= roleDefinitions.value(key).permissions();
        } else {
            qWarning() << "RoleStore: Peer" << peerId
                       << "is assigned unknown role:" << roleNameStd;
        }
    }
    return finalPermissions;
}

void RoleStore::setLocalPeerId(const QString& peerId) {
    bool changed = false;
    QString newIdVal; // To emit the correct value after unlock
    {                 // Scope for the locker
        QWriteLocker locker(&storeLock);
        if (this->localPeerId != peerId) {
            this->localPeerId = peerId;
            newIdVal          = this->localPeerId; // Store for emitting
            changed           = true;
            qInfo() << "RoleStore: Local Peer ID set to:" << this->localPeerId;
        }
    } // locker goes out of scope and releases the lock here

    if (changed) {
        emit localPeerIdConfirmed(newIdVal);
    }
}

QString RoleStore::getLocalPeerId() const {
    QReadLocker locker(&storeLock);
    return localPeerId;
}

QVector<QString> RoleStore::getKnownPeerIds() const {
    QReadLocker locker(&storeLock);
    QSet<QString> ids;
    for (auto it = peerAssignments.constBegin();
         it != peerAssignments.constEnd(); ++it) {
        ids.insert(it.key());
    }
    for (auto it = peerUsernames.constBegin(); it != peerUsernames.constEnd();
         ++it) {
        ids.insert(it.key());
    }
    return QVector<QString>::fromList(ids.values().toVector());
}

void RoleStore::storePeerUsername(const QString& peerId,
                                  const QString& username) {
    if (peerId.isEmpty())
        return;
    bool changed = false;
    {
        QWriteLocker locker(&storeLock);
        if (peerUsernames.value(peerId) != username) {
            if (!username.isEmpty()) {
                peerUsernames[peerId] = username;
            } else {
                peerUsernames.remove(peerId);
            }
            changed = true;
        }
    }
    if (changed) {
        emit allAssignmentsRefreshed();
    }
}

QString RoleStore::getPeerUsername(const QString& peerId) const {
    if (peerId.isEmpty())
        return "";
    QReadLocker locker(&storeLock);
    return peerUsernames.value(peerId, ""); // Return empty if not found
}

} // namespace Roles
} // namespace P2P
