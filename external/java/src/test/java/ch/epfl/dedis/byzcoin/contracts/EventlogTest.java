package ch.epfl.dedis.byzcoin.contracts;

import ch.epfl.dedis.byzcoin.ByzCoinRPC;
import ch.epfl.dedis.byzcoin.InstanceId;
import ch.epfl.dedis.byzcoin.SignerCounters;
import ch.epfl.dedis.eventlog.Event;
import ch.epfl.dedis.eventlog.EventLogInstance;
import ch.epfl.dedis.eventlog.SearchResponse;
import ch.epfl.dedis.integration.TestServerController;
import ch.epfl.dedis.integration.TestServerInit;
import ch.epfl.dedis.lib.Hex;
import ch.epfl.dedis.lib.darc.*;
import ch.epfl.dedis.lib.exception.CothorityCommunicationException;
import ch.epfl.dedis.lib.exception.CothorityCryptoException;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;

import static ch.epfl.dedis.byzcoin.ByzCoinRPCTest.BLOCK_INTERVAL;
import static org.junit.jupiter.api.Assertions.*;

class EventLogTest {
    private static ByzCoinRPC bc;
    private static EventLogInstance el;
    private static Signer admin;
    private static Darc genesisDarc;

    private final static Logger logger = LoggerFactory.getLogger(EventLogTest.class);
    private TestServerController testInstanceController;

    @BeforeEach
    void initAll() throws Exception {
        testInstanceController = TestServerInit.getInstance();
        // We use a fixed private key for the test to make sure there is no randomness in the genesis darc ID,
        // which is a requirement to use name resolution later.
        admin = new SignerEd25519(Hex.parseHexBinary("76F40BEA4681B898E49D9657682703C0C3AA5D677A1DD259BDC60A66376B9E00"));
        genesisDarc = ByzCoinRPC.makeGenesisDarc(admin, testInstanceController.getRoster());
        genesisDarc.addIdentity("spawn:eventlog", admin.getIdentity(), Rules.OR);
        genesisDarc.addIdentity("invoke:" + EventLogInstance.ContractId + "." + EventLogInstance.LogCmd, admin.getIdentity(), Rules.OR);
        genesisDarc.addIdentity("_name:" + EventLogInstance.ContractId, admin.getIdentity(), Rules.OR);

        bc = new ByzCoinRPC(testInstanceController.getRoster(), genesisDarc, BLOCK_INTERVAL);
        if (!bc.checkLiveness()) {
            throw new CothorityCommunicationException("liveness check failed");
        }

        // Get the counter for the admin
        SignerCounters adminCtrs = bc.getSignerCounters(Collections.singletonList(admin.getIdentity().toString()));
        adminCtrs.increment();
        el = new EventLogInstance(bc, genesisDarc.getId(), Arrays.asList(admin), adminCtrs.getCounters());

        // Initialise the naming instance, we do not store this instance but we get it later using fromByzCoin
        adminCtrs.increment();
        new NamingInstance(bc, genesisDarc.getId(), Collections.singletonList(admin), adminCtrs.getCounters());
    }

    @Test
    void log() throws Exception {
        // Get the counter for the admin
        SignerCounters adminCtrs = bc.getSignerCounters(Collections.singletonList(admin.getIdentity().toString()));
        adminCtrs.increment();

        Event e = new Event("hello", "goodbye");
        InstanceId key = el.log(e, Arrays.asList(admin), adminCtrs.getCounters());
        Thread.sleep(5 * bc.getConfig().getBlockInterval().toMillis());
        Event loggedEvent = el.get(key);
        assertEquals(loggedEvent, e);
    }

    @Test
    void logMore() throws Exception {
        // Get the counter for the admin
        SignerCounters adminCtrs = bc.getSignerCounters(Collections.singletonList(admin.getIdentity().toString()));

        int n = 50;
        List<InstanceId> keys = new ArrayList<>(n);
        Event event = new Event("login", "alice");
        for (int i = 0; i < n; i++) {
            // The timestamps in these event are all the same, but doing el.log takes time and it may not be possible to
            // add all the events. So we have to limit the number of events that we add.
            adminCtrs.increment();
            keys.add(el.log(event, Arrays.asList(admin), adminCtrs.getCounters()));
        }
        boolean allOK = true;
        for (int i = 0; i < 4; i++) {
            allOK = true;
            Thread.sleep(5 * bc.getConfig().getBlockInterval().toMillis());
            for (InstanceId key : keys) {
                try {
                    logger.info("ok");
                    // this checks the trie proofs.
                    Event event2 = el.get(key);
                    assertEquals(event, event2);
                } catch (CothorityCryptoException e){
                    logger.info("bad");
                    allOK = false;
                    break;
                }
            }
            if (allOK){
                break;
            }
        }
        assertTrue(allOK, "one of the events failed");

        // check that we can't get an event that doesn't exist
        InstanceId badKey = new InstanceId(Hex.parseHexBinary("CDC4FB0BDD74CD86410DC80C818E7A0DB3C6452C9161CF7C6FC16D00C5CF0DA7"));
        assertThrows(CothorityCryptoException.class, () -> el.get(badKey));

        // Try to reconnect after doing a lot of transactions.
        ByzCoinRPC.fromByzCoin(bc.getRoster(), bc.getGenesisBlock().getId());
    }

    @Test
    void search() throws Exception {
        // Get the counter for the admin
        SignerCounters adminCtrs = bc.getSignerCounters(Collections.singletonList(admin.getIdentity().toString()));
        adminCtrs.increment();

        long now = System.currentTimeMillis() * 1000 * 1000;
        Event event = new Event(now, "login", "alice");
        el.log(event, Collections.singletonList(admin), adminCtrs.getCounters());

        Thread.sleep(5 * bc.getConfig().getBlockInterval().toMillis());

        // finds the event under any topic
        SearchResponse resp = el.search("", now - 1000, now + 1000);
        assertEquals(1, resp.events.size());
        assertEquals(resp.events.get(0), event);
        assertFalse(resp.truncated);

        // finds the event under the right topic
        resp = el.search("login", now - 1000, now + 1000);
        assertEquals(1, resp.events.size());
        assertEquals(resp.events.get(0), event);
        assertFalse(resp.truncated);

        // event does not exist
        resp = el.search("", now - 2000, now - 1000);
        assertEquals(0, resp.events.size());
    }

    class TestEventHandler implements EventLogInstance.EventHandler {
        public List<Event> events = new ArrayList<>();
        public List<String> errors = new ArrayList<>();
        @Override
        public void process(Event e, byte[] id) {
            events.add(e);
        }
        @Override
        public void error(String s) {
            errors.add(s);
        }
    }

    @Test
    void subscribe() throws Exception {
        TestEventHandler handler = new TestEventHandler();
        int tag = el.subscribeEvents(handler);

        SignerCounters adminCtrs = bc.getSignerCounters(Collections.singletonList(admin.getIdentity().toString()));
        adminCtrs.increment();
        List<Event> events = new ArrayList<>();
        events.add(new Event("hello", "goodbye"));
        events.add(new Event("bonjour", "au revoir"));
        events.add(new Event("hola", "adios"));

        el.log(events, Arrays.asList(admin), adminCtrs.getCounters());
        Thread.sleep(5 * bc.getConfig().getBlockInterval().toMillis());

        for (int i = 0; i < events.size(); i++) {
            assertEquals(events.get(i), handler.events.get(i));
        }
        assertEquals(0, handler.errors.size());

        el.unsubscribeEvents(tag);
    }

    @Test
    void subscribeFrom() throws Exception {
        SignerCounters adminCtrs = bc.getSignerCounters(Collections.singletonList(admin.getIdentity().toString()));
        adminCtrs.increment();
        List<Event> events = new ArrayList<>();
        events.add(new Event("hello", "goodbye"));
        events.add(new Event("bonjour", "au revoir"));
        events.add(new Event("hola", "adios"));

        // log one event without the handler
        el.log(events.subList(0, 1), Arrays.asList(admin), adminCtrs.getCounters());
        Thread.sleep(5 * bc.getConfig().getBlockInterval().toMillis());

        // subscribe from the genesis block
        TestEventHandler handler = new TestEventHandler();
        int tag = el.subscribeEvents(handler, bc.getGenesisBlock().getHash());

        // log two more events with the handler
        adminCtrs.increment();
        el.log(events.subList(1, 3), Arrays.asList(admin), adminCtrs.getCounters());
        Thread.sleep(5 * bc.getConfig().getBlockInterval().toMillis());

        // check that all the events are logged
        for (int i = 0; i < events.size(); i++) {
            assertEquals(events.get(i), handler.events.get(i));
        }
        assertEquals(0, handler.errors.size());

        el.unsubscribeEvents(tag);
    }

    @Test
    void subscribeFailure() {
        // subscribe from a block that doesn't exist
        TestEventHandler handler = new TestEventHandler();
        assertThrows(CothorityCommunicationException.class, () -> el.subscribeEvents(handler, Hex.parseHexBinary("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")));

        // remove tag that doesn't exist should not throw an error
        el.unsubscribeEvents(9999);
    }

    @Test
    void nameResolution() throws Exception {
        SignerCounters adminCtrs = bc.getSignerCounters(Collections.singletonList(admin.getIdentity().toString()));
        adminCtrs.increment();
        NamingInstance namingInstance = NamingInstance.fromByzcoin(bc);
        namingInstance.setAndWait("my event log", el.getInstanceId(), Arrays.asList(admin), adminCtrs.getCounters(), 10);

        // We use a fixed genesis ID so make sure there is no randomness in the genesis darc between test executions.
        InstanceId iID = bc.resolveInstanceID(new DarcId(Hex.parseHexBinary("DA74C7FBE9AB0ADCF9BBFF797AA8F2012BC624ABEBCAE9900CD9DE6A0679B19F")), "my event log");
        assertEquals(iID, el.getInstanceId());

        // Remove it and the resolution should fail.
        adminCtrs.increment();
        namingInstance.removeAndWait("my event log", el.getInstanceId(), Arrays.asList(admin), adminCtrs.getCounters(), 10);
        assertThrows(CothorityCommunicationException.class,
                () -> bc.resolveInstanceID(new DarcId(Hex.parseHexBinary("DA74C7FBE9AB0ADCF9BBFF797AA8F2012BC624ABEBCAE9900CD9DE6A0679B19F")), "my event log"));
    }
}
